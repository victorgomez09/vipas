package k3s

import (
	"context"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"

	"github.com/victorgomez09/vipas/apps/api/internal/orchestrator"
)

var (
	gatewayClassGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}
	gatewayGVR      = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	envoyProxyGVR   = schema.GroupVersionResource{Group: "gateway.envoyproxy.io", Version: "v1alpha1", Resource: "envoyproxies"}
)

// intstrFromInt is a tiny helper to avoid importing intstr in multiple places.
func intstrFromInt(i int) intstr.IntOrString { return intstr.FromInt(i) }

// EnsureGateway ensures the GatewayClass and central Gateway exist (idempotent).
func (o *Orchestrator) EnsureGateway(ctx context.Context) error {
	// Create GatewayClass if needed
	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	// Ensure gateway-system namespace exists
	if err := o.ensureNamespace(ctx, "gateway-system"); err != nil {
		return fmt.Errorf("ensure gateway ns: %w", err)
	}

	if err := o.ensureEnvoyProxy(ctx, dyn); err != nil {
		return fmt.Errorf("ensure envoyproxy: %w", err)
	}

	// GatewayClass
	gcName := "envoy-gateway"
	gcObj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "GatewayClass",
		"metadata": map[string]interface{}{
			"name": gcName,
		},
		"spec": map[string]interface{}{
			"controllerName": "gateway.envoyproxy.io/gatewayclass-controller",
			"parametersRef": map[string]interface{}{
				"group":     "gateway.envoyproxy.io",
				"kind":      "EnvoyProxy",
				"name":      "vipas-proxy",
				"namespace": "gateway-system",
			},
		},
	}}
	existingGC, gcErr := dyn.Resource(gatewayClassGVR).Get(ctx, gcName, metav1.GetOptions{})
	if gcErr != nil {
		if _, createErr := dyn.Resource(gatewayClassGVR).Create(ctx, gcObj, metav1.CreateOptions{}); createErr != nil {
			o.logger.Warn("create gatewayclass failed", slog.Any("error", createErr))
		} else {
			o.logger.Info("gatewayclass created", slog.String("name", gcName))
		}
	} else {
		gcObj.SetResourceVersion(existingGC.GetResourceVersion())
		if _, updateErr := dyn.Resource(gatewayClassGVR).Update(ctx, gcObj, metav1.UpdateOptions{}); updateErr != nil {
			o.logger.Warn("update gatewayclass failed", slog.Any("error", updateErr))
		}
	}

	// Ensure Gateway
	gwName := "vipas-gateway"
	_, err = dyn.Resource(gatewayGVR).Namespace("gateway-system").Get(ctx, gwName, metav1.GetOptions{})
	if err == nil {
		return nil
	}

	gw := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "Gateway",
		"metadata": map[string]interface{}{
			"name":      gwName,
			"namespace": "gateway-system",
			"labels": map[string]interface{}{
				"app.kubernetes.io/managed-by": "vipas",
			},
		},
		"spec": map[string]interface{}{
			"gatewayClassName": gcName,
			"listeners": []interface{}{
				map[string]interface{}{
					"name":          "http",
					"port":          int64(80),
					"protocol":      "HTTP",
					"allowedRoutes": map[string]interface{}{"namespaces": map[string]interface{}{"from": "All"}},
				},
				map[string]interface{}{
					"name":          "https",
					"port":          int64(443),
					"protocol":      "HTTPS",
					"tls":           map[string]interface{}{"mode": "Terminate"},
					"allowedRoutes": map[string]interface{}{"namespaces": map[string]interface{}{"from": "All"}},
				},
			},
		},
	}}
	if _, err := dyn.Resource(gatewayGVR).Namespace("gateway-system").Create(ctx, gw, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create gateway: %w", err)
	}
	o.logger.Info("gateway created", slog.String("name", gwName))

	// Ensure Envoy dataplane service is exposed as a LoadBalancer so Cilium can assign
	// an external IP reachable from the host. The dataplane runs in namespace
	// `envoy-gateway-system` and pods are owned by the Gateway controller with labels
	// `gateway.envoyproxy.io/owning-gateway-name=vipas-gateway` and
	// `gateway.envoyproxy.io/owning-gateway-namespace=gateway-system`.
	svcNS := "envoy-gateway-system"
	// Ensure the namespace exists (installed by Helm normally)
	_ = o.ensureNamespace(ctx, svcNS)

	svcName := fmt.Sprintf("envoy-gateway-%s", gwName)
	// Create Service if missing
	if _, err := o.client.CoreV1().Services(svcNS).Get(ctx, svcName, metav1.GetOptions{}); k8serrors.IsNotFound(err) {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      svcName,
				Namespace: svcNS,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "vipas",
					"vipas/lb-type":                "bgp",
				},
			},
			Spec: corev1.ServiceSpec{
				Type: corev1.ServiceTypeLoadBalancer,
				Selector: map[string]string{
					"gateway.envoyproxy.io/owning-gateway-name":      gwName,
					"gateway.envoyproxy.io/owning-gateway-namespace": "gateway-system",
				},
				Ports: []corev1.ServicePort{
					{Port: 80, TargetPort: intstrFromInt(80), Protocol: corev1.ProtocolTCP},
					{Port: 443, TargetPort: intstrFromInt(443), Protocol: corev1.ProtocolTCP},
				},
			},
		}
		if _, err := o.client.CoreV1().Services(svcNS).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
			o.logger.Warn("failed to create envoy gateway Service", slog.Any("error", err))
		} else {
			o.logger.Info("created LoadBalancer Service for envoy gateway", slog.String("service", svcName), slog.String("ns", svcNS))
		}
	}
	return nil
}

func (o *Orchestrator) ensureEnvoyProxy(ctx context.Context, dyn dynamic.Interface) error {
	nodes, err := o.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("list nodes: %w", err)
	}

	hasGatewayNodes := false
	for _, n := range nodes.Items {
		if n.Labels["vipas/pool"] == "gateway" {
			hasGatewayNodes = true
			break
		}
	}

	obj := defaultEnvoyProxyObj()
	if hasGatewayNodes {
		obj = gatewayNodesEnvoyProxyObj()
	}

	current, err := dyn.Resource(envoyProxyGVR).Namespace("gateway-system").Get(ctx, "vipas-proxy", metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			if _, createErr := dyn.Resource(envoyProxyGVR).Namespace("gateway-system").Create(ctx, obj, metav1.CreateOptions{}); createErr != nil {
				return createErr
			}
			o.logger.Info("envoyproxy created", slog.Bool("gateway_nodes", hasGatewayNodes))
			return nil
		}
		return err
	}

	obj.SetResourceVersion(current.GetResourceVersion())
	if _, err := dyn.Resource(envoyProxyGVR).Namespace("gateway-system").Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		return err
	}
	o.logger.Info("envoyproxy updated", slog.Bool("gateway_nodes", hasGatewayNodes))
	return nil
}

func defaultEnvoyProxyObj() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.envoyproxy.io/v1alpha1",
		"kind":       "EnvoyProxy",
		"metadata": map[string]interface{}{
			"name":      "vipas-proxy",
			"namespace": "gateway-system",
		},
		"spec": map[string]interface{}{
			"provider": map[string]interface{}{
				"type": "Kubernetes",
				"kubernetes": map[string]interface{}{
					"envoyDeployment": map[string]interface{}{
						"patch": map[string]interface{}{
							"type": "StrategicMerge",
							"value": map[string]interface{}{
								"spec": map[string]interface{}{
									"strategy": map[string]interface{}{"type": "Recreate"},
									"template": map[string]interface{}{
										"spec": map[string]interface{}{
											"hostNetwork": true,
											"dnsPolicy":   "ClusterFirstWithHostNet",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}}
}

func gatewayNodesEnvoyProxyObj() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.envoyproxy.io/v1alpha1",
		"kind":       "EnvoyProxy",
		"metadata": map[string]interface{}{
			"name":      "vipas-proxy",
			"namespace": "gateway-system",
		},
		"spec": map[string]interface{}{
			"provider": map[string]interface{}{
				"type": "Kubernetes",
				"kubernetes": map[string]interface{}{
					"envoyDaemonSet": map[string]interface{}{
						"patch": map[string]interface{}{
							"type": "StrategicMergePatch",
							"value": map[string]interface{}{
								"spec": map[string]interface{}{
									"template": map[string]interface{}{
										"spec": map[string]interface{}{
											"nodeSelector": map[string]interface{}{
												"vipas/pool": "gateway",
											},
											"tolerations": []interface{}{
												map[string]interface{}{
													"key":    "role",
													"value":  "gateway",
													"effect": "NoSchedule",
												},
											},
											"hostNetwork": true,
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}}
}

// EnsureGateway implements orchestrator.GatewayManager (declared for compile-time check)
var _ orchestrator.GatewayManager = (*Orchestrator)(nil)

// GetGatewayIP returns the external IP assigned to the Envoy Gateway.
// It reads the status.addresses field of the "vipas-gateway" Gateway resource.
// Returns an empty string (no error) when the Gateway has no address yet.
func (o *Orchestrator) GetGatewayIP(ctx context.Context) (string, error) {
	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return "", fmt.Errorf("dynamic client: %w", err)
	}

	gw, err := dyn.Resource(gatewayGVR).Namespace("gateway-system").Get(ctx, "vipas-gateway", metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get gateway: %w", err)
	}

	addrs, found, _ := unstructured.NestedSlice(gw.Object, "status", "addresses")
	if !found || len(addrs) == 0 {
		return "", nil
	}

	if addr, ok := addrs[0].(map[string]interface{}); ok {
		if ip, ok := addr["value"].(string); ok {
			return ip, nil
		}
	}

	return "", nil
}
