package k3s

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/victorgomez09/vipas/apps/api/internal/orchestrator"
)

var (
	ciliumLBPoolGVR     = schema.GroupVersionResource{Group: "cilium.io", Version: "v2alpha1", Resource: "ciliumloadbalancerippools"}
	ciliumL2PolicyGVR   = schema.GroupVersionResource{Group: "cilium.io", Version: "v2alpha1", Resource: "ciliuml2announcementpolicies"}
	vipasLBPoolName     = "vipas-lb-pool"         // Nombre del pool de IPs para Cilium Load Balancer
	vipasL2PolicyName   = "vipas-l2-announcement" // Nombre de la política de anuncio L2
	managedByLabel      = "app.kubernetes.io/managed-by"
	managedByLabelValue = "vipas"
)

// EnsureLoadBalancer configures Cilium LB resources for cilium-l2.
// lbType accepts: cilium-l2, nodeport.
func (o *Orchestrator) EnsureLoadBalancer(ctx context.Context, lbType, ipPool string) error {
	lbType = normalizeLBType(lbType)
	if lbType == "" {
		lbType = "nodeport"
	}
	if lbType == "nodeport" {
		o.logger.Info("EnsureLoadBalancer: skipping, nodeport mode")
		return nil
	}
	if lbType != "cilium-l2" {
		return fmt.Errorf("unsupported lb type %q", lbType)
	}
	ipPool = strings.TrimSpace(ipPool)
	if ipPool == "" {
		return fmt.Errorf("ip pool is required for %s", lbType)
	}

	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	// Ensure a Cilium LB IP pool exists for Gateway Service allocations.
	pool := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cilium.io/v2alpha1",
		"kind":       "CiliumLoadBalancerIPPool",
		"metadata": map[string]interface{}{
			"name": vipasLBPoolName,
			"labels": map[string]interface{}{
				managedByLabel: managedByLabelValue,
			},
		},
		"spec": map[string]interface{}{
			"blocks": []interface{}{
				map[string]interface{}{"cidr": ipPool},
			},
		},
	}}
	if err := upsertClusterResource(ctx, dyn, ciliumLBPoolGVR, vipasLBPoolName, pool); err != nil {
		return fmt.Errorf("ensure cilium lb pool: %w", err)
	}

	l2 := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "cilium.io/v2alpha1",
		"kind":       "CiliumL2AnnouncementPolicy",
		"metadata": map[string]interface{}{
			"name": vipasL2PolicyName,
			"labels": map[string]interface{}{
				managedByLabel: managedByLabelValue,
			},
		},
		"spec": map[string]interface{}{
			"serviceSelector": map[string]interface{}{
				"matchLabels": map[string]interface{}{
					managedByLabel: managedByLabelValue,
				},
			},
			"loadBalancerIPs": true,
		},
	}}
	if err := upsertClusterResource(ctx, dyn, ciliumL2PolicyGVR, vipasL2PolicyName, l2); err != nil {
		return fmt.Errorf("ensure cilium l2 announcement policy: %w", err)
	}
	o.logger.Info("configured cilium l2 load balancer", slog.String("pool", ipPool))

	return nil
}

// GetLoadBalancerStatus returns configured pools and assigned LoadBalancer IPs.
func (o *Orchestrator) GetLoadBalancerStatus(ctx context.Context) (*orchestrator.LBStatus, error) {
	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}

	// List configured Cilium LB pools.
	pools, _ := dyn.Resource(ciliumLBPoolGVR).List(ctx, metav1.ListOptions{})
	var poolNames []string
	if pools != nil {
		for _, p := range pools.Items {
			blocks, found, _ := unstructured.NestedSlice(p.Object, "spec", "blocks")
			if !found || len(blocks) == 0 {
				poolNames = append(poolNames, p.GetName())
				continue
			}
			for _, b := range blocks {
				bMap, ok := b.(map[string]interface{})
				if !ok {
					continue
				}
				if cidr, ok := bMap["cidr"].(string); ok && strings.TrimSpace(cidr) != "" {
					poolNames = append(poolNames, cidr)
				}
			}
		}
	}

	// Find Services of type LoadBalancer and collect assigned IPs
	svcList, err := o.client.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	seenAssigned := map[string]struct{}{}
	var assigned []string
	for _, s := range svcList.Items {
		if s.Spec.Type == "LoadBalancer" {
			for _, ing := range s.Status.LoadBalancer.Ingress {
				if ing.IP != "" {
					if _, ok := seenAssigned[ing.IP]; !ok {
						assigned = append(assigned, ing.IP)
						seenAssigned[ing.IP] = struct{}{}
					}
				}
				if ing.Hostname != "" {
					if _, ok := seenAssigned[ing.Hostname]; !ok {
						assigned = append(assigned, ing.Hostname)
						seenAssigned[ing.Hostname] = struct{}{}
					}
				}
			}
		}
	}

	l2PolicyCount := 0
	if l2List, err := dyn.Resource(ciliumL2PolicyGVR).List(ctx, metav1.ListOptions{}); err == nil {
		l2PolicyCount = len(l2List.Items)
	}

	lbType := "nodeport"
	if l2PolicyCount > 0 {
		lbType = "cilium-l2"
	}

	return &orchestrator.LBStatus{Type: lbType, IPPools: poolNames, AssignedIPs: assigned, BGPPeers: []orchestrator.BGPPeerInfo{}}, nil
}

func normalizeLBType(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "", "nodeport", "cilium-l2":
		return v
	case "l2", "cilium-l2-announcement":
		return "cilium-l2"
	default:
		return v
	}
}

func upsertClusterResource(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, name string, obj *unstructured.Unstructured) error {
	current, err := dyn.Resource(gvr).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) {
			_, createErr := dyn.Resource(gvr).Create(ctx, obj, metav1.CreateOptions{})
			return createErr
		}
		return err
	}
	obj.SetResourceVersion(current.GetResourceVersion())
	_, err = dyn.Resource(gvr).Update(ctx, obj, metav1.UpdateOptions{})
	return err
}

func deleteClusterResourceIfExists(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, name string) error {
	err := dyn.Resource(gvr).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		return err
	}
	return nil
}

// Ensure k8s compile-time check for interface
var _ orchestrator.LoadBalancerManager = (*Orchestrator)(nil)
