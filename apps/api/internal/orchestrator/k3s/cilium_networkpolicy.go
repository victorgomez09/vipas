package k3s

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// cilium_networkpolicy.go
// Implementación inicial para gestionar CiliumNetworkPolicy mediante el client dynamic.

// EnsureCiliumNetworkPolicy crea/borra una política Cilium por namespace.
// Crea una política básica default-deny (L3/L4) permitiendo tráfico desde el mismo
// namespace y `kube-system`, y egress para DNS. Más adelante añadiremos L7 y FQDN.
func (o *Orchestrator) EnsureCiliumNetworkPolicy(ctx context.Context, namespace string, enabled bool) error {
	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	gvr := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
	res := dyn.Resource(gvr).Namespace(namespace)
	name := vipasNetPolName

	if !enabled {
		err := res.Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("delete cilium np: %w", err)
		}
		o.logger.Info("removed CiliumNetworkPolicy", slog.String("ns", namespace))
		return nil
	}

	// Start building spec with default L3/L4 rules
	spec := map[string]interface{}{
		"endpointSelector": map[string]interface{}{},
		"ingress": []interface{}{
			map[string]interface{}{
				"fromEndpoints": []interface{}{
					map[string]interface{}{
						"matchLabels": map[string]interface{}{
							"kubernetes.io/metadata.name": namespace,
						},
					},
				},
			},
			map[string]interface{}{
				"fromEndpoints": []interface{}{
					map[string]interface{}{
						"matchLabels": map[string]interface{}{
							"kubernetes.io/metadata.name": "envoy-gateway-system",
						},
					},
				},
			},
			map[string]interface{}{
				"fromEndpoints": []interface{}{
					map[string]interface{}{
						"matchLabels": map[string]interface{}{
							"kubernetes.io/metadata.name": "kube-system",
						},
					},
				},
			},
			map[string]interface{}{
				"fromEndpoints": []interface{}{
					map[string]interface{}{
						"matchLabels": map[string]interface{}{
							"kubernetes.io/metadata.name": "gateway-system",
						},
					},
				},
			},
		},
		"egress": []interface{}{
			map[string]interface{}{
				"toPorts": []interface{}{
					map[string]interface{}{
						"ports": []interface{}{
							map[string]interface{}{"port": "53", "protocol": "UDP"},
							map[string]interface{}{"port": "53", "protocol": "TCP"},
						},
					},
				},
			},
		},
	}

	// Read optional ConfigMap `vipas-networkpolicy` in the namespace for L7 and FQDN rules.
	cm, err := o.client.CoreV1().ConfigMaps(namespace).Get(ctx, "vipas-networkpolicy", metav1.GetOptions{})
	if err == nil && cm != nil {
		// FQDN allowlist: key `allow_fqdns` comma-separated
		if v, ok := cm.Data["allow_fqdns"]; ok && strings.TrimSpace(v) != "" {
			fqdns := []interface{}{}
			for _, part := range strings.Split(v, ",") {
				m := strings.TrimSpace(part)
				if m == "" {
					continue
				}
				fqdns = append(fqdns, map[string]interface{}{"matchName": m})
			}
			if len(fqdns) > 0 {
				// Allow egress to these FQDNs (HTTPS/HTTP)
				egressRule := map[string]interface{}{}
				egressRule["toFQDNs"] = fqdns
				// allow ports 80 and 443
				egressRule["toPorts"] = []interface{}{
					map[string]interface{}{
						"ports": []interface{}{
							map[string]interface{}{"port": "80", "protocol": "TCP"},
							map[string]interface{}{"port": "443", "protocol": "TCP"},
						},
					},
				}
				spec["egress"] = append(spec["egress"].([]interface{}), egressRule)
			}
		}

		// HTTP L7 rules: key `http_paths` JSON: { "service-name": [ {"port":80, "paths":["/","/health"]} ] }
		if v, ok := cm.Data["http_paths"]; ok && strings.TrimSpace(v) != "" {
			var svcMap map[string][]struct {
				Port  int      `json:"port"`
				Paths []string `json:"paths"`
			}
			if err := json.Unmarshal([]byte(v), &svcMap); err == nil {
				// For each service entry, add ingress toPorts rule with HTTP l7 rules
				for svcName, entries := range svcMap {
					for _, e := range entries {
						// Build http rules
						httpRules := []interface{}{}
						for _, p := range e.Paths {
							httpRules = append(httpRules, map[string]interface{}{"path": p})
						}

						toPorts := map[string]interface{}{
							"ports": []interface{}{
								map[string]interface{}{"port": fmt.Sprintf("%d", e.Port), "protocol": "TCP"},
							},
							"rules": map[string]interface{}{
								"http": httpRules,
							},
						}

						ingressRule := map[string]interface{}{
							// allow from same namespace and kube-system already defined; append toPorts
							"toPorts": []interface{}{toPorts},
						}

						spec["ingress"] = append(spec["ingress"].([]interface{}), ingressRule)
						// Note: we could scope by fromEndpoints matching service selector, but
						// service selectors vary; L7 rules applied to ports provide path filtering.
						_ = svcName // svcName kept for future use
					}
				}
			} else {
				o.logger.Info("invalid http_paths JSON in vipas-networkpolicy", slog.String("ns", namespace), slog.Any("err", err))
			}
		}
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cilium.io/v2",
			"kind":       "CiliumNetworkPolicy",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "vipas",
				},
			},
			"spec": spec,
		},
	}

	_, err = res.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			_, err = res.Create(ctx, obj, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("create cilium np: %w", err)
			}
			o.logger.Info("created CiliumNetworkPolicy", slog.String("ns", namespace))
			return nil
		}
		return fmt.Errorf("get cilium np: %w", err)
	}

	_, err = res.Update(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update cilium np: %w", err)
	}
	o.logger.Info("updated CiliumNetworkPolicy", slog.String("ns", namespace))
	return nil
}

// BuildCiliumNetworkPolicyPayload permanece como helper por compatibilidad; devuelve error
// para indicar que ahora se usa el método principal que opera con dynamic client.
func (o *Orchestrator) BuildCiliumNetworkPolicyPayload(namespace string) (interface{}, error) {
	return nil, fmt.Errorf("deprecated: use EnsureCiliumNetworkPolicy which applies the resource directly")
}

// ApplyCiliumNetworkPolicy queda como wrapper futuro para aplicar payloads genéricos.
func (o *Orchestrator) ApplyCiliumNetworkPolicy(ctx context.Context, payload interface{}) error {
	return fmt.Errorf("not implemented: ApplyCiliumNetworkPolicy")
}
