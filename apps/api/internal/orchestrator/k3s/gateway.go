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
	gatewayClassGVR = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}
	gatewayGVR      = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
)

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

	// GatewayClass
	gcName := "envoy-gateway"
	_, err = dyn.Resource(gatewayClassGVR).Get(ctx, gcName, metav1.GetOptions{})
	if err != nil {
		// Create minimal GatewayClass
		gc := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": "gateway.networking.k8s.io/v1",
			"kind":       "GatewayClass",
			"metadata": map[string]interface{}{
				"name": gcName,
			},
			"spec": map[string]interface{}{
				"controllerName": "gateway.envoyproxy.io/gatewayclass-controller",
			},
		}}
		if _, createErr := dyn.Resource(gatewayClassGVR).Create(ctx, gc, metav1.CreateOptions{}); createErr != nil {
			o.logger.Warn("create gatewayclass failed", slog.Any("error", createErr))
		} else {
			o.logger.Info("gatewayclass created", slog.String("name", gcName))
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
	return nil
}

// EnsureGateway implements orchestrator.GatewayManager (declared for compile-time check)
var _ orchestrator.GatewayManager = (*Orchestrator)(nil)
