package k3s

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CreateOrUpdateSecret creates or updates a secret in the given namespace.
func (o *Orchestrator) CreateOrUpdateSecret(ctx context.Context, namespace, name string, data map[string][]byte) error {
	// Ensure namespace exists
	if err := o.ensureNamespace(ctx, namespace); err != nil {
		return fmt.Errorf("ensure namespace: %w", err)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"vipas/managed-by": "vipas",
			},
		},
		Data: data,
		Type: corev1.SecretTypeOpaque,
	}

	existing, err := o.client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			_, err = o.client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
			return err
		}
		return err
	}
	existing.Data = data
	_, err = o.client.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// EnsureExternalDNS installs or upgrades external-dns via helm with basic options.
func (o *Orchestrator) EnsureExternalDNS(ctx context.Context, provider, zone, apiKeyRef string) error {
	// Build helm args
	args := []string{"upgrade", "--install", "external-dns", "external-dns/external-dns", "-n", "external-dns", "--create-namespace", "--wait", "--timeout", "180s"}
	if provider != "" {
		args = append(args, "--set", fmt.Sprintf("provider=%s", provider))
	}
	// Use gateway httproute source
	args = append(args, "--set", "sources={gateway-httproute}")
	// Domain filter
	if strings.TrimSpace(zone) != "" {
		args = append(args, "--set", fmt.Sprintf("domainFilters={%s}", zone))
	}
	// If apiKeyRef provided, pass it as value (chart-specific handling left to chart values).
	if apiKeyRef != "" {
		args = append(args, "--set", fmt.Sprintf("secretName=%s", apiKeyRef))
	}

	// Ensure the external-dns repo exists (best-effort)
	_ = exec.CommandContext(ctx, "helm", "repo", "add", "external-dns", "https://kubernetes-sigs.github.io/external-dns/").Run()
	_ = exec.CommandContext(ctx, "helm", "repo", "update").Run()

	cmd := exec.CommandContext(ctx, "helm", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm external-dns failed: %v: %s", err, string(out))
	}
	o.logger.Info("external-dns helm applied", "output", string(out))
	return nil
}

// Orchestrator implements the orchestrator.Orchestrator interface (compile-time check)
