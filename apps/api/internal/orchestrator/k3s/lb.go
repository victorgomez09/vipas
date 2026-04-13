package k3s

import (
	"context"
	"fmt"
	"log/slog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/victorgomez09/vipas/apps/api/internal/orchestrator"
)

var (
	ipPoolGVR  = schema.GroupVersionResource{Group: "metallb.io", Version: "v1beta1", Resource: "ipaddresspools"}
	l2AdvGVR   = schema.GroupVersionResource{Group: "metallb.io", Version: "v1beta1", Resource: "l2advertisements"}
	bgpPeerGVR = schema.GroupVersionResource{Group: "metallb.io", Version: "v1beta1", Resource: "bgppeers"}
)

// EnsureLoadBalancer configures MetalLB resources when lbType == "metallb".
func (o *Orchestrator) EnsureLoadBalancer(ctx context.Context, lbType, ipPool string) error {
	if lbType != "metallb" {
		o.logger.Info("EnsureLoadBalancer: skipping, lb type not metallb", slog.String("type", lbType))
		return nil
	}

	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	// Ensure metallb-system namespace exists
	if err := o.ensureNamespace(ctx, "metallb-system"); err != nil {
		return fmt.Errorf("ensure metallb namespace: %w", err)
	}

	// Create IPAddressPool
	poolName := "vipas-ip-pool"
	ipp := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "metallb.io/v1beta1",
		"kind":       "IPAddressPool",
		"metadata": map[string]interface{}{
			"name":      poolName,
			"namespace": "metallb-system",
			"labels":    map[string]interface{}{"app.kubernetes.io/managed-by": "vipas"},
		},
		"spec": map[string]interface{}{
			"addresses": []interface{}{ipPool},
		},
	}}

	if _, err := dyn.Resource(ipPoolGVR).Namespace("metallb-system").Get(ctx, poolName, metav1.GetOptions{}); err != nil {
		if _, cErr := dyn.Resource(ipPoolGVR).Namespace("metallb-system").Create(ctx, ipp, metav1.CreateOptions{}); cErr != nil {
			o.logger.Warn("create ipaddresspool failed", slog.Any("error", cErr))
		} else {
			o.logger.Info("ipaddresspool created", slog.String("pool", ipPool))
		}
	}

	// Create L2Advertisement for layer2 operation (if not present)
	l2Name := "vipas-l2"
	l2 := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "metallb.io/v1beta1",
		"kind":       "L2Advertisement",
		"metadata": map[string]interface{}{
			"name":      l2Name,
			"namespace": "metallb-system",
			"labels":    map[string]interface{}{"app.kubernetes.io/managed-by": "vipas"},
		},
		"spec": map[string]interface{}{},
	}}
	if _, err := dyn.Resource(l2AdvGVR).Namespace("metallb-system").Get(ctx, l2Name, metav1.GetOptions{}); err != nil {
		if _, cErr := dyn.Resource(l2AdvGVR).Namespace("metallb-system").Create(ctx, l2, metav1.CreateOptions{}); cErr != nil {
			o.logger.Warn("create l2advertisement failed", slog.Any("error", cErr))
		} else {
			o.logger.Info("l2advertisement created")
		}
	}

	return nil
}

// GetLoadBalancerStatus returns configured pools and assigned LoadBalancer IPs.
func (o *Orchestrator) GetLoadBalancerStatus(ctx context.Context) (*orchestrator.LBStatus, error) {
	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return nil, fmt.Errorf("create dynamic client: %w", err)
	}

	// List IPAddressPools
	pools, _ := dyn.Resource(ipPoolGVR).Namespace("metallb-system").List(ctx, metav1.ListOptions{})
	var poolNames []string
	for _, p := range pools.Items {
		poolNames = append(poolNames, p.GetName())
	}

	// Find Services of type LoadBalancer and collect assigned IPs
	svcList, err := o.client.CoreV1().Services("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	var assigned []string
	for _, s := range svcList.Items {
		if s.Spec.Type == "LoadBalancer" {
			for _, ing := range s.Status.LoadBalancer.Ingress {
				if ing.IP != "" {
					assigned = append(assigned, ing.IP)
				}
				if ing.Hostname != "" {
					assigned = append(assigned, ing.Hostname)
				}
			}
		}
	}

	// List BGPPeers (if any)
	var peers []orchestrator.BGPPeerInfo
	if bpList, err := dyn.Resource(bgpPeerGVR).Namespace("metallb-system").List(ctx, metav1.ListOptions{}); err == nil {
		for _, it := range bpList.Items {
			spec := it.Object["spec"]
			var peerAddr string
			var peerASN int64
			var srcAddr string
			if specMap, ok := spec.(map[string]interface{}); ok {
				if v, ok := specMap["peerAddress"]; ok {
					if s, ok := v.(string); ok {
						peerAddr = s
					}
				}
				if v, ok := specMap["peerASN"]; ok {
					switch val := v.(type) {
					case int64:
						peerASN = val
					case int:
						peerASN = int64(val)
					case float64:
						peerASN = int64(val)
					}
				}
				if v, ok := specMap["sourceAddress"]; ok {
					if s, ok := v.(string); ok {
						srcAddr = s
					}
				}
			}
			peers = append(peers, orchestrator.BGPPeerInfo{
				Name:        it.GetName(),
				PeerAddress: peerAddr,
				PeerASN:     peerASN,
				SourceAddr:  srcAddr,
			})
		}
	}

	return &orchestrator.LBStatus{Type: "metallb", IPPools: poolNames, AssignedIPs: assigned, BGPPeers: peers}, nil
}

// Ensure k8s compile-time check for interface
var _ orchestrator.LoadBalancerManager = (*Orchestrator)(nil)
