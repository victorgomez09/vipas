package k3s

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/victorgomez09/vipas/apps/api/internal/orchestrator"
)

var (
	// MetalLB GVRs
	metalLBIPPoolGVR  = schema.GroupVersionResource{Group: "metallb.io", Version: "v1beta1", Resource: "ipaddresspools"}
	metalLBL2AdvGVR   = schema.GroupVersionResource{Group: "metallb.io", Version: "v1beta1", Resource: "l2advertisements"}
	metalLBBGPPeerGVR = schema.GroupVersionResource{Group: "metallb.io", Version: "v1beta2", Resource: "bgppeers"} // Use v1beta2 for BGPPeer

	vipasMetalLBIPPoolName = "vipas-metallb-pool"
	vipasMetalLBL2AdvName  = "vipas-metallb-l2-advertisement"
	managedByLabel         = "app.kubernetes.io/managed-by"
	managedByLabelValue    = "vipas"
)

// EnsureLoadBalancer configures MetalLB resources for the cluster.
// lbType accepts: metallb, nodeport.
func (o *Orchestrator) EnsureLoadBalancer(ctx context.Context, lbType, ipPool string) error {
	lbType = strings.ToLower(strings.TrimSpace(lbType))
	if lbType == "" {
		lbType = "nodeport"
	}

	ipPool = strings.TrimSpace(ipPool)

	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	// Handle nodeport mode: delete MetalLB resources if they exist
	if lbType == "nodeport" {
		o.logger.Info("EnsureLoadBalancer: skipping, nodeport mode. Deleting MetalLB resources if they exist.")
		_ = deleteClusterResourceIfExists(ctx, dyn, metalLBIPPoolGVR, vipasMetalLBIPPoolName)
		_ = deleteClusterResourceIfExists(ctx, dyn, metalLBL2AdvGVR, vipasMetalLBL2AdvName)
		return nil
	}

	if lbType != "metallb" {
		return fmt.Errorf("unsupported lb type %q", lbType)
	}

	if ipPool == "" {
		return fmt.Errorf("ip pool is required for %s", lbType)
	}

	// Ensure a MetalLB IPAddressPool exists
	pool := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "metallb.io/v1beta1",
		"kind":       "IPAddressPool",
		"metadata": map[string]interface{}{
			"name": vipasMetalLBIPPoolName,
			"labels": map[string]interface{}{
				managedByLabel: managedByLabelValue,
			},
		},
		"spec": map[string]interface{}{
			"addresses":     []interface{}{ipPool},
			"autoAssign":    true,
			"avoidBuggyIPs": true,
			// This service selector ensures the pool is used by the Envoy Gateway Service
			// which has the label vipas-service-type: "external-proxy"
			"serviceAllocation": map[string]interface{}{
				"serviceSelectors": []interface{}{
					map[string]interface{}{
						"matchLabels": map[string]interface{}{
							"vipas-service-type": "external-proxy",
						},
					},
				},
			},
		},
	}}
	if err := upsertClusterResource(ctx, dyn, metalLBIPPoolGVR, vipasMetalLBIPPoolName, pool); err != nil {
		return fmt.Errorf("ensure metallb ip address pool: %w", err)
	}

	// Ensure an L2Advertisement exists for the IP pool
	l2Adv := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "metallb.io/v1beta1",
		"kind":       "L2Advertisement",
		"metadata": map[string]interface{}{
			"name": vipasMetalLBL2AdvName,
			"labels": map[string]interface{}{
				managedByLabel: managedByLabelValue,
			},
		},
		"spec": map[string]interface{}{
			"ipAddressPools": []interface{}{vipasMetalLBIPPoolName},
			// interfaces: []interface{}{"eth0"}, // Optional: specify interfaces if needed
		},
	}}
	if err := upsertClusterResource(ctx, dyn, metalLBL2AdvGVR, vipasMetalLBL2AdvName, l2Adv); err != nil {
		return fmt.Errorf("ensure metallb l2 advertisement: %w", err)
	}
	o.logger.Info("configured MetalLB load balancer", slog.String("pool", ipPool))

	return nil
}

// GetLoadBalancerStatus returns configured pools and assigned LoadBalancer IPs.
func (o *Orchestrator) GetLoadBalancerStatus(ctx context.Context) (*orchestrator.LBStatus, error) {
	status := &orchestrator.LBStatus{
		Type:        "nodeport",
		IPPools:     []string{},
		AssignedIPs: []string{},
		BGPPeers:    []orchestrator.BGPPeerInfo{},
	}

	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return status, nil // Return default status if dynamic client fails
	}

	// 1. Check MetalLB IPAddressPools
	if list, err := dyn.Resource(metalLBIPPoolGVR).List(ctx, metav1.ListOptions{}); err == nil && len(list.Items) > 0 {
		status.Type = "metallb"
		for _, item := range list.Items {
			if addresses, found, _ := unstructured.NestedSlice(item.Object, "spec", "addresses"); found {
				for _, addr := range addresses {
					if s, ok := addr.(string); ok {
						status.IPPools = append(status.IPPools, s)
					}
				}
			}
		}
	} else {
		// Fallback to nodeport if no MetalLB pools are found
		status.Type = "nodeport"
	}

	// 2. Collect assigned IPs from all Services
	svcList, err := o.client.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err == nil {
		seen := make(map[string]bool)
		for _, s := range svcList.Items {
			if s.Spec.Type == corev1.ServiceTypeLoadBalancer {
				for _, ing := range s.Status.LoadBalancer.Ingress {
					ip := ing.IP
					if ip == "" {
						ip = ing.Hostname
					}
					if ip != "" && !seen[ip] {
						status.AssignedIPs = append(status.AssignedIPs, ip)
						seen[ip] = true
					}
				}
			}
		}
	}

	// 3. Collect BGP Peers (if any are configured for MetalLB)
	if list, err := dyn.Resource(metalLBBGPPeerGVR).List(ctx, metav1.ListOptions{}); err == nil {
		for _, item := range list.Items {
			if addr, ok, _ := unstructured.NestedString(item.Object, "spec", "peerAddress"); ok {
				asn, _, _ := unstructured.NestedInt64(item.Object, "spec", "peerASN")
				status.BGPPeers = append(status.BGPPeers, orchestrator.BGPPeerInfo{
					Name:        item.GetName(),
					PeerAddress: addr,
					PeerASN:     asn,
				})
			}
		}
	}

	return status, nil
}

// normalizeLBType is no longer needed as the logic is now directly in EnsureLoadBalancer.
// Removed.

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
