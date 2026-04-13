package k3s

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"log/slog"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/dynamic"

	"github.com/victorgomez09/vipas/apps/api/internal/model"
	"github.com/victorgomez09/vipas/apps/api/internal/orchestrator"
)

var (
	httpRouteGVR   = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	certificateGVR = schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
)

func httpRouteName(appName, host string) string {
	sanitized := fmt.Sprintf("%s-%s", appName, sanitize(host))
	if len(sanitized) <= 63 {
		return sanitized
	}
	full := fmt.Sprintf("%s-%s", appName, host)
	h := sha256.Sum256([]byte(full))
	suffix := hex.EncodeToString(h[:4])
	return sanitized[:63-9] + "-" + suffix
}

func legacyHTTPRouteName(appName, host string) string {
	name := fmt.Sprintf("%s-%s", appName, sanitize(host))
	if len(name) > 63 {
		return name[:63]
	}
	return name
}

// certificateName returns a safe name for cert-manager Certificate in the app namespace.
func certificateName(appName, host string) string {
	sanitized := fmt.Sprintf("%s-%s", appName, sanitize(host))
	if len(sanitized) <= 63 {
		return sanitized
	}
	full := fmt.Sprintf("%s-%s", appName, host)
	h := sha256.Sum256([]byte(full))
	suffix := hex.EncodeToString(h[:4])
	return sanitized[:63-9] + "-" + suffix
}

// CreateHTTPRoute creates or updates an HTTPRoute pointing to the app Service.
func (o *Orchestrator) CreateHTTPRoute(ctx context.Context, domain *model.Domain, app *model.Application) error {
	ns := appNamespace(app)
	name := httpRouteName(appK8sName(app), domain.Host)

	backendPort := int64(80)
	if len(app.Ports) > 0 {
		backendPort = int64(app.Ports[0].ServicePort)
		if backendPort == 0 {
			backendPort = int64(app.Ports[0].ContainerPort)
		}
	}

	labels := map[string]interface{}{
		"app.kubernetes.io/managed-by": "vipas",
		"vipas/app-id":                 app.ID.String(),
		"vipas/domain-id":              domain.ID.String(),
	}

	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	// Build HTTPRoute unstructured
	hr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": ns,
			"labels":    labels,
		},
		"spec": map[string]interface{}{
			"parentRefs": []interface{}{
				map[string]interface{}{"name": "vipas-gateway", "namespace": "gateway-system", "sectionName": "http"},
			},
			"hostnames": []interface{}{domain.Host},
			"rules": []interface{}{
				map[string]interface{}{
					"matches": []interface{}{map[string]interface{}{"path": map[string]interface{}{"type": "PathPrefix", "value": "/"}}},
					"backendRefs": []interface{}{
						map[string]interface{}{"name": appK8sName(app), "port": backendPort, "weight": int64(1)},
					},
				},
			},
		},
	}}

	// (no-op marker removed)

	// First, try to find an existing HTTPRoute for this app (label vipas/app-id=<app.ID>). If one
	// exists, add the hostname to its spec.hostnames (if not already present) and update it. This
	// avoids creating many routes per-app and keeps hostnames consolidated.
	if list, lerr := dyn.Resource(httpRouteGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("vipas/app-id=%s", app.ID.String())}); lerr == nil && len(list.Items) > 0 {
		existing := list.Items[0]
		// read existing hostnames
		hosts, found, _ := unstructured.NestedStringSlice(existing.Object, "spec", "hostnames")
		if !found {
			hosts = []string{}
		}
		// append if missing
		present := false
		for _, h := range hosts {
			if h == domain.Host {
				present = true
				break
			}
		}
		if !present {
			hosts = append(hosts, domain.Host)
			if err := unstructured.SetNestedStringSlice(existing.Object, hosts, "spec", "hostnames"); err != nil {
				return fmt.Errorf("set hostnames: %w", err)
			}
			if _, uerr := dyn.Resource(httpRouteGVR).Namespace(ns).Update(ctx, &existing, metav1.UpdateOptions{}); uerr != nil {
				return fmt.Errorf("update httproute hostnames: %w", uerr)
			}
			o.logger.Info("httproute updated (added hostname)", slog.String("host", domain.Host), slog.String("ns", ns))
		} else {
			o.logger.Info("httproute already contains hostname", slog.String("host", domain.Host), slog.String("ns", ns))
		}

	} else {
		// No existing consolidated route — create a new one named by host
		if _, err := dyn.Resource(httpRouteGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{}); err != nil {
			if _, createErr := dyn.Resource(httpRouteGVR).Namespace(ns).Create(ctx, hr, metav1.CreateOptions{}); createErr != nil {
				return fmt.Errorf("create httproute: %w", createErr)
			}
			o.logger.Info("httproute created", slog.String("host", domain.Host), slog.String("ns", ns))
		}
	}

	// If TLS is requested and auto-cert is enabled, create a cert-manager Certificate
	// Skip certificate creation for common dev domains (localhost, .local, nip.io, sslip.io, test)
	if domain.TLS && domain.AutoCert {
		h := strings.ToLower(strings.TrimSpace(domain.Host))
		isDev := strings.Contains(h, "nip.io") || strings.Contains(h, "sslip.io") || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".test") || strings.Contains(h, "traefik.me")
		if !isDev {
			certName := certificateName(appK8sName(app), domain.Host)
			secretName := fmt.Sprintf("%s-tls", certName)
			cert := &unstructured.Unstructured{Object: map[string]interface{}{
				"apiVersion": "cert-manager.io/v1",
				"kind":       "Certificate",
				"metadata": map[string]interface{}{
					"name":      certName,
					"namespace": ns,
					"labels":    labels,
				},
				"spec": map[string]interface{}{
					"secretName": secretName,
					"dnsNames":   []interface{}{domain.Host},
					// Default to letsencrypt-staging; cluster admin can change SettingCertIssuer later
					"issuerRef": map[string]interface{}{"name": "letsencrypt-staging", "kind": "ClusterIssuer"},
				},
			}}

			if _, gErr := dyn.Resource(certificateGVR).Namespace(ns).Get(ctx, certName, metav1.GetOptions{}); gErr != nil {
				if _, cErr := dyn.Resource(certificateGVR).Namespace(ns).Create(ctx, cert, metav1.CreateOptions{}); cErr != nil {
					o.logger.Warn("failed to create certificate", slog.String("host", domain.Host), slog.Any("error", cErr))
				} else {
					o.logger.Info("certificate created", slog.String("host", domain.Host), slog.String("ns", ns))
				}
			} else {
				// Update existing cert (best-effort)
				if _, uErr := dyn.Resource(certificateGVR).Namespace(ns).Update(ctx, cert, metav1.UpdateOptions{}); uErr != nil {
					o.logger.Warn("failed to update certificate", slog.String("host", domain.Host), slog.Any("error", uErr))
				} else {
					o.logger.Info("certificate updated", slog.String("host", domain.Host), slog.String("ns", ns))
				}
			}
		}
	}

	return nil
}

func (o *Orchestrator) UpdateHTTPRoute(ctx context.Context, domain *model.Domain, app *model.Application) error {
	return o.CreateHTTPRoute(ctx, domain, app)
}

func (o *Orchestrator) DeleteHTTPRoute(ctx context.Context, domain *model.Domain) error {
	// Delete HTTPRoutes labeled with domain id across namespaces
	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return err
	}
	nsList, err := o.client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	var lastErr error
	for _, ns := range nsList.Items {
		list, lerr := dyn.Resource(httpRouteGVR).Namespace(ns.Name).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("vipas/domain-id=%s", domain.ID.String())})
		if lerr != nil {
			continue
		}
		for _, item := range list.Items {
			if derr := dyn.Resource(httpRouteGVR).Namespace(ns.Name).Delete(ctx, item.GetName(), metav1.DeleteOptions{}); derr != nil {
				lastErr = derr
			} else {
				o.logger.Info("httproute deleted", slog.String("name", item.GetName()), slog.String("ns", ns.Name))
			}
		}
	}
	return lastErr
}

func (o *Orchestrator) DeleteHTTPRouteByName(ctx context.Context, app *model.Application, name string) error {
	ns := appNamespace(app)
	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return err
	}
	if err := dyn.Resource(httpRouteGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return err
	}
	o.logger.Info("httproute deleted by name", slog.String("name", name), slog.String("ns", ns))
	return nil
}

func (o *Orchestrator) HTTPRouteName(app *model.Application, host string) string {
	return httpRouteName(appK8sName(app), host)
}

func (o *Orchestrator) LegacyRouteName(app *model.Application, host string) string {
	return legacyHTTPRouteName(appK8sName(app), host)
}

func (o *Orchestrator) SyncHTTPRoutePorts(ctx context.Context, app *model.Application) error {
	// Update backend port on HTTPRoutes labeled for the app.
	ns := appNamespace(app)
	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return err
	}
	list, err := dyn.Resource(httpRouteGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("vipas/app-id=%s", app.ID.String())})
	if err != nil {
		return nil
	}
	backendPort := int64(80)
	if len(app.Ports) > 0 {
		if app.Ports[0].ServicePort != 0 {
			backendPort = int64(app.Ports[0].ServicePort)
		} else {
			backendPort = int64(app.Ports[0].ContainerPort)
		}
	}
	for _, item := range list.Items {
		// Modify .spec.rules[*].backendRefs[*].port if different
		spec, ok := item.Object["spec"].(map[string]interface{})
		if !ok {
			continue
		}
		rules, ok := spec["rules"].([]interface{})
		if !ok {
			continue
		}
		updated := false
		for ri := range rules {
			rule := rules[ri].(map[string]interface{})
			brs, ok := rule["backendRefs"].([]interface{})
			if !ok {
				continue
			}
			for bi := range brs {
				br := brs[bi].(map[string]interface{})
				if p, ok := br["port"].(int64); ok {
					if p != backendPort {
						br["port"] = backendPort
						updated = true
					}
				} else if pFloat, ok := br["port"].(float64); ok {
					if int64(pFloat) != backendPort {
						br["port"] = backendPort
						updated = true
					}
				}
				brs[bi] = br
			}
			rule["backendRefs"] = brs
			rules[ri] = rule
		}
		if updated {
			spec["rules"] = rules
			item.Object["spec"] = spec
			if _, err := dyn.Resource(httpRouteGVR).Namespace(ns).Update(ctx, &item, metav1.UpdateOptions{}); err != nil {
				o.logger.Warn("failed to sync httproute port", slog.String("name", item.GetName()), slog.Any("error", err))
			}
		}
	}
	return nil
}

func (o *Orchestrator) GetHTTPRouteStatus(ctx context.Context, domain *model.Domain, app *model.Application) (*orchestrator.RouteStatus, error) {
	ns := appNamespace(app)
	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return &orchestrator.RouteStatus{Ready: false, Message: err.Error()}, nil
	}
	list, err := dyn.Resource(httpRouteGVR).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: fmt.Sprintf("vipas/domain-id=%s", domain.ID.String())})
	if err != nil || len(list.Items) == 0 {
		return &orchestrator.RouteStatus{Ready: false, Message: "httproute not found"}, nil
	}
	rt := list.Items[0]
	// Check status.parents[].conditions for Accepted or Programmed
	status, _, _ := unstructured.NestedSlice(rt.Object, "status", "parents")
	ready := false
	if status != nil {
		for _, p := range status {
			if pm, ok := p.(map[string]interface{}); ok {
				if conds, ok := pm["conditions"].([]interface{}); ok {
					for _, c := range conds {
						if cm, ok := c.(map[string]interface{}); ok {
							if t, _ := cm["type"].(string); t == "Accepted" {
								if s, _ := cm["status"].(string); s == "True" {
									ready = true
								}
							}
						}
					}
				}
			}
		}
	}
	return &orchestrator.RouteStatus{Ready: ready}, nil
}

func (o *Orchestrator) GetCertExpiry(ctx context.Context, domain *model.Domain, app *model.Application) (*time.Time, error) {
	// Try to find TLS secrets in the app namespace and parse the certificate
	ns := appNamespace(app)
	secrets, err := o.client.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	var earliest *time.Time
	for _, s := range secrets.Items {
		if s.Type != corev1.SecretTypeTLS {
			continue
		}
		crtData, ok := s.Data["tls.crt"]
		if !ok || len(crtData) == 0 {
			continue
		}
		certs, err := parseCertsFromPEM(crtData)
		if err != nil || len(certs) == 0 {
			continue
		}
		for _, c := range certs {
			// Check if cert matches domain host (CN or SAN)
			if certMatchesHost(c, domain.Host) {
				exp := c.NotAfter
				if earliest == nil || exp.Before(*earliest) {
					earliest = &exp
				}
			}
		}
	}
	return earliest, nil
}

// parseCertsFromPEM parses one or more PEM encoded certs
func parseCertsFromPEM(pemBytes []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			// skip unparsable blocks
			continue
		}
		certs = append(certs, c)
	}
	return certs, nil
}

// certMatchesHost checks whether the certificate is valid for the given host.
func certMatchesHost(cert *x509.Certificate, host string) bool {
	if host == "" {
		return false
	}
	if err := cert.VerifyHostname(host); err == nil {
		return true
	}
	// Fallback: check CommonName
	if cert.Subject.CommonName == host {
		return true
	}
	return false
}

func (o *Orchestrator) DeletePanelHTTPRoute(ctx context.Context) error {
	// Delete HTTPRoute and panel Service/Endpoints
	ns := "vipas"
	name := "vipas-panel"
	// Delete HTTPRoute
	dyn, err := dynamic.NewForConfig(o.config)
	if err == nil {
		_ = dyn.Resource(httpRouteGVR).Namespace(ns).Delete(ctx, name, metav1.DeleteOptions{})
	}
	// Delete Service
	_ = o.client.CoreV1().Services(ns).Delete(ctx, name, metav1.DeleteOptions{})
	_ = o.client.CoreV1().Endpoints(ns).Delete(ctx, name, metav1.DeleteOptions{})
	o.logger.Info("panel httproute and service deleted")
	return nil
}

func (o *Orchestrator) EnsurePanelHTTPRoute(ctx context.Context, domain, httpsEmail string) error {
	// Ensure the panel namespace exists
	if err := o.ensureNamespace(ctx, "vipas"); err != nil {
		return fmt.Errorf("panel httproute: %w", err)
	}

	// Ensure K8s Service + Endpoints pointing to the host (Vipas runs in Docker, not K8s)
	if err := o.ensurePanelService(ctx); err != nil {
		return fmt.Errorf("panel httproute: %w", err)
	}

	// Build HTTPRoute object for panel
	ns := "vipas"
	name := "vipas-panel"
	labels := map[string]interface{}{"app.kubernetes.io/managed-by": "vipas", "app.kubernetes.io/component": "panel"}
	hr := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": ns,
			"labels":    labels,
		},
		"spec": map[string]interface{}{
			"parentRefs": []interface{}{
				map[string]interface{}{"name": "vipas-gateway", "namespace": "gateway-system", "sectionName": "http"},
			},
			"hostnames": []interface{}{domain},
			"rules": []interface{}{
				map[string]interface{}{
					"matches": []interface{}{map[string]interface{}{"path": map[string]interface{}{"type": "PathPrefix", "value": "/"}}},
					"backendRefs": []interface{}{
						map[string]interface{}{"name": "vipas", "port": int64(3000), "weight": int64(1)},
					},
				},
			},
		},
	}}

	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	if _, err := dyn.Resource(httpRouteGVR).Namespace(ns).Get(ctx, name, metav1.GetOptions{}); k8serrors.IsNotFound(err) {
		if _, cErr := dyn.Resource(httpRouteGVR).Namespace(ns).Create(ctx, hr, metav1.CreateOptions{}); cErr != nil {
			return fmt.Errorf("create panel httproute: %w", cErr)
		}
		o.logger.Info("panel httproute created", slog.String("domain", domain))
		return nil
	} else if err != nil {
		return fmt.Errorf("get panel httproute: %w", err)
	}

	if _, uErr := dyn.Resource(httpRouteGVR).Namespace(ns).Update(ctx, hr, metav1.UpdateOptions{}); uErr != nil {
		return fmt.Errorf("update panel httproute: %w", uErr)
	}
	o.logger.Info("panel httproute updated", slog.String("domain", domain))
	return nil
}

// ensurePanelService creates a headless Service + Endpoints in the panel namespace
// pointing to the host's IP:3000 (Vipas runs as a Docker container, not a K8s Pod).
func (o *Orchestrator) ensurePanelService(ctx context.Context) error {
	svcName := "vipas"
	port := int32(3000)

	// Get node IP for the endpoint
	nodes, err := o.client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil || len(nodes.Items) == 0 {
		return fmt.Errorf("no nodes found")
	}
	var nodeIP string
	for _, addr := range nodes.Items[0].Status.Addresses {
		if addr.Type == corev1.NodeInternalIP {
			nodeIP = addr.Address
			break
		}
	}
	if nodeIP == "" {
		return fmt.Errorf("cannot determine node IP")
	}

	// Ensure Service
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: "vipas",
			Labels:    map[string]string{"app.kubernetes.io/managed-by": "vipas"},
		},
		Spec: corev1.ServiceSpec{
			Type:  corev1.ServiceTypeClusterIP,
			Ports: []corev1.ServicePort{{Port: port, TargetPort: intstr.FromInt(int(port))}},
		},
	}
	_, err = o.client.CoreV1().Services("vipas").Create(ctx, svc, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("create panel service: %w", err)
	}

	// Ensure Endpoints
	ep := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{Name: svcName, Namespace: "vipas"},
		Subsets:    []corev1.EndpointSubset{{Addresses: []corev1.EndpointAddress{{IP: nodeIP}}, Ports: []corev1.EndpointPort{{Port: port}}}},
	}
	_, err = o.client.CoreV1().Endpoints("vipas").Create(ctx, ep, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return fmt.Errorf("create panel endpoints: %w", err)
	}
	return nil
}
