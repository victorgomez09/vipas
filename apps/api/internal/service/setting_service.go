package service

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"

	"github.com/victorgomez09/vipas/apps/api/internal/model"
	"github.com/victorgomez09/vipas/apps/api/internal/orchestrator"
	"github.com/victorgomez09/vipas/apps/api/internal/store"
)

type SettingService struct {
	store  store.Store
	orch   orchestrator.Orchestrator
	logger *slog.Logger
}

func NewSettingService(s store.Store, orch orchestrator.Orchestrator, logger *slog.Logger) *SettingService {
	return &SettingService{store: s, orch: orch, logger: logger}
}

// InitDefaults runs on server startup. Detects K3s node IP and sets default base domain.
func (s *SettingService) InitDefaults(ctx context.Context) error {
	done, _ := s.store.Settings().Get(ctx, model.SettingSetupDone)
	if done == "true" {
		return nil
	}

	// Try to get IP from K3s control-plane node first
	ip := s.detectK3sNodeIP(ctx)
	if ip == "" {
		// Fallback to local network detection
		ip = detectLocalIP()
	}

	if ip != "" {
		_ = s.store.Settings().Set(ctx, model.SettingServerIP, ip)
		s.logger.Info("detected server IP", slog.String("ip", ip))

		existing, _ := s.store.Settings().Get(ctx, model.SettingBaseDomain)
		if existing == "" {
			// Prefer the gateway external IP as the base domain anchor when available.
			baseDomainIP := ip
			if gwIP := s.detectGatewayIP(ctx); gwIP != "" {
				baseDomainIP = gwIP
			}
			baseDomain := fmt.Sprintf("%s.sslip.io", baseDomainIP)
			_ = s.store.Settings().Set(ctx, model.SettingBaseDomain, baseDomain)
			s.logger.Info("set default base domain", slog.String("domain", baseDomain))
		}
	}

	_ = s.store.Settings().Set(ctx, model.SettingSetupDone, "true")
	return nil
}

// ReconcileInfra re-applies infrastructure state on every startup.
// This ensures panel ingress, HTTPS redirect middleware, and other K8s
// resources survive restarts, accidental deletions, or cleanup operations.
func (s *SettingService) ReconcileInfra(ctx context.Context) {
	// Refresh the current external IP assigned to the Envoy Gateway on every boot.
	if gwIP := s.detectGatewayIP(ctx); gwIP != "" {
		if err := s.store.Settings().Set(ctx, model.SettingGatewayIP, gwIP); err != nil {
			s.logger.Warn("reconcile: failed to store gateway IP", slog.Any("error", err))
		} else {
			s.logger.Info("gateway IP refreshed", slog.String("ip", gwIP))
		}
	}

	// Re-apply panel ingress if a domain is configured
	if err := s.applyPanelDomain(ctx, s.getPanelDomain(ctx)); err != nil {
		s.logger.Warn("reconcile: panel ingress not applied", slog.Any("error", err))
	}
}

func (s *SettingService) getPanelDomain(ctx context.Context) string {
	val, _ := s.store.Settings().Get(ctx, model.SettingPanelDomain)
	return val
}

// detectGatewayIP queries the Envoy Gateway status to get its current external IP.
// Returns an empty string when the gateway is not yet ready or not running on k3s.
func (s *SettingService) detectGatewayIP(ctx context.Context) string {
	ip, err := s.orch.GetGatewayIP(ctx)
	if err != nil {
		s.logger.Debug("detectGatewayIP: could not read gateway IP", slog.Any("error", err))
		return ""
	}
	return ip
}

// detectK3sNodeIP gets the InternalIP of the first control-plane node.
func (s *SettingService) detectK3sNodeIP(ctx context.Context) string {
	nodes, err := s.orch.GetNodes(ctx)
	if err != nil || len(nodes) == 0 {
		return ""
	}

	// Prefer control-plane node IP
	for _, node := range nodes {
		if node.IP != "" {
			for _, role := range node.Roles {
				if role == "control-plane" || role == "master" {
					return node.IP
				}
			}
		}
	}

	// Fallback: any node with an IP
	for _, node := range nodes {
		if node.IP != "" {
			return node.IP
		}
	}

	return ""
}

// GetBaseDomain returns the configured base domain.
func (s *SettingService) GetBaseDomain(ctx context.Context) string {
	val, _ := s.store.Settings().Get(ctx, model.SettingBaseDomain)
	return val
}

func (s *SettingService) GetServerIP(ctx context.Context) string {
	val, _ := s.store.Settings().Get(ctx, "server_ip")
	return val
}

func (s *SettingService) GetAll(ctx context.Context) ([]model.Setting, error) {
	return s.store.Settings().GetAll(ctx)
}

func (s *SettingService) Get(ctx context.Context, key string) (string, error) {
	return s.store.Settings().Get(ctx, key)
}

func (s *SettingService) Set(ctx context.Context, key, value string) error {
	value = strings.TrimSpace(value)

	if key == model.SettingLBType {
		normalized := normalizeLBType(value)
		if value != "" && normalized == "" {
			return fmt.Errorf("invalid lb_type: %q (allowed: cilium-l2, nodeport)", value)
		}
		value = normalized
	}

	if err := s.store.Settings().Set(ctx, key, value); err != nil {
		return err
	}
	s.logger.Info("setting updated", slog.String("key", key))

	// Apply side effects — setting is saved, but warn caller if infra failed
	switch key {
	case model.SettingPanelDomain:
		if err := s.applyPanelDomain(ctx, value); err != nil {
			s.logger.Warn("panel domain saved but ingress not applied", slog.Any("error", err))
			return fmt.Errorf("setting saved, but ingress not applied: %w", err)
		}
	case model.SettingHTTPSEmail:
		if err := s.applyHTTPSEmail(ctx, value); err != nil {
			s.logger.Warn("HTTPS email saved but not applied", slog.Any("error", err))
			return fmt.Errorf("setting saved, but HTTPS config not applied: %w", err)
		}
	case model.SettingCertIssuer:
		// Validate known issuers (best-effort) and re-apply panel route so cert-manager can pick new issuer
		if value != "letsencrypt-staging" && value != "letsencrypt-prod" && value != "selfsigned" && value != "" {
			s.logger.Warn("unknown cert issuer set", slog.String("value", value))
		}
		if err := s.applyCertIssuer(ctx, value); err != nil {
			s.logger.Warn("cert issuer saved but not applied", slog.Any("error", err))
			return fmt.Errorf("setting saved, but cert issuer not applied: %w", err)
		}
	case model.SettingLBType, model.SettingLBIPPool:
		// Re-apply load balancer configuration (best-effort)
		lbType, _ := s.store.Settings().Get(ctx, model.SettingLBType)
		ipPool, _ := s.store.Settings().Get(ctx, model.SettingLBIPPool)
		if err := s.applyLoadBalancerConfig(ctx, lbType, ipPool); err != nil {
			s.logger.Warn("lb setting saved but not applied", slog.Any("error", err))
			return fmt.Errorf("setting saved, but LB config not applied: %w", err)
		}
	}

	// DNS settings: validate and persist; installer handles deployment during install.
	switch key {
	case model.SettingDNSProvider:
		if value != "" {
			if !isValidDNSProvider(value) {
				return fmt.Errorf("invalid dns_provider: %q (allowed: cloudflare, route53, digitalocean, coredns, pihole, manual)", value)
			}
		}
		// Orchestrate external-dns at runtime when provider changes.
		// Retrieve current zone and api_key_ref and apply.
		zone, _ := s.store.Settings().Get(ctx, model.SettingDNSZone)
		apiRef, _ := s.store.Settings().Get(ctx, model.SettingDNSAPIKeyRef)
		if err := s.orch.EnsureExternalDNS(ctx, value, zone, apiRef); err != nil {
			s.logger.Warn("dns provider saved but external-dns orchestration failed", slog.Any("error", err))
			return fmt.Errorf("setting saved, but external-dns not applied: %w", err)
		}
	case model.SettingDNSZone:
		prov, _ := s.store.Settings().Get(ctx, model.SettingDNSProvider)
		apiRef, _ := s.store.Settings().Get(ctx, model.SettingDNSAPIKeyRef)
		if prov != "" {
			if err := s.orch.EnsureExternalDNS(ctx, prov, value, apiRef); err != nil {
				s.logger.Warn("dns zone saved but external-dns orchestration failed", slog.Any("error", err))
				return fmt.Errorf("setting saved, but external-dns not applied: %w", err)
			}
		}
	case model.SettingDNSAPIKeyRef:
		// When API key ref is updated, re-apply external-dns so the chart picks up credentials.
		prov, _ := s.store.Settings().Get(ctx, model.SettingDNSProvider)
		zone, _ := s.store.Settings().Get(ctx, model.SettingDNSZone)
		if prov != "" {
			if err := s.orch.EnsureExternalDNS(ctx, prov, zone, value); err != nil {
				s.logger.Warn("dns api key ref saved but external-dns orchestration failed", slog.Any("error", err))
				return fmt.Errorf("setting saved, but external-dns not applied: %w", err)
			}
		}
	}

	return nil
}

// applyPanelDomain creates or removes the panel HTTPRoute.
func (s *SettingService) applyPanelDomain(ctx context.Context, domain string) error {
	if domain == "" {
		return s.orch.DeletePanelHTTPRoute(ctx)
	}
	httpsEmail, _ := s.store.Settings().Get(ctx, model.SettingHTTPSEmail)
	return s.orch.EnsurePanelHTTPRoute(ctx, domain, httpsEmail)
}

// applyHTTPSEmail updates HTTPS issuer settings and re-applies panel route if configured.
func (s *SettingService) applyHTTPSEmail(ctx context.Context, email string) error {
	// Also re-apply panel HTTPRoute if a domain is configured, so TLS picks up the email
	panelDomain, _ := s.store.Settings().Get(ctx, model.SettingPanelDomain)
	if panelDomain != "" {
		return s.orch.EnsurePanelHTTPRoute(ctx, panelDomain, email)
	}
	return nil
}

// applyCertIssuer re-applies panel HTTPRoute so that cert-manager annotations
// or configuration pick up the selected issuer. No-op if no panel domain is set.
func (s *SettingService) applyCertIssuer(ctx context.Context, issuer string) error {
	panelDomain, _ := s.store.Settings().Get(ctx, model.SettingPanelDomain)
	if panelDomain == "" {
		return nil
	}
	// Re-ensure panel route; EnsurePanelHTTPRoute will use current settings stored
	httpsEmail, _ := s.store.Settings().Get(ctx, model.SettingHTTPSEmail)
	return s.orch.EnsurePanelHTTPRoute(ctx, panelDomain, httpsEmail)
}

// applyLoadBalancerConfig applies LB settings. When lbType is empty it defaults
// to cilium-l2 for single-node clusters (dev) and cilium-bgp for multi-node (prod).
func (s *SettingService) applyLoadBalancerConfig(ctx context.Context, lbType, ipPool string) error {
	if lbType == "" {
		lbType = s.defaultLBTypeByTopology(ctx)
		if err := s.store.Settings().Set(ctx, model.SettingLBType, lbType); err != nil {
			s.logger.Warn("failed to persist inferred lb_type", slog.Any("error", err), slog.String("lb_type", lbType))
		}
	}

	ipPool = strings.TrimSpace(ipPool)
	if (lbType == "cilium-l2" || lbType == "cilium-bgp") && ipPool == "" {
		return fmt.Errorf("lb_ip_pool is required for %s", lbType)
	}

	return s.orch.EnsureLoadBalancer(ctx, lbType, ipPool)
}

func (s *SettingService) defaultLBTypeByTopology(ctx context.Context) string {
	return "cilium-l2"
}

func normalizeLBType(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return ""
	}
	if v == "l2" || v == "cilium-l2-announcement" {
		return "cilium-l2"
	}
	if slices.Contains([]string{"cilium-l2", "nodeport"}, v) {
		return v
	}
	return ""
}

// SMTPConfig holds SMTP mail server settings.
type SMTPConfig struct {
	Host     string `json:"host"`
	Port     string `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	From     string `json:"from"`
	Enabled  bool   `json:"enabled"`
}

// GetSMTPConfig reads SMTP settings from the settings table.
func (s *SettingService) GetSMTPConfig(ctx context.Context) (*SMTPConfig, error) {
	host, _ := s.store.Settings().Get(ctx, "smtp_host")
	port, _ := s.store.Settings().Get(ctx, "smtp_port")
	user, _ := s.store.Settings().Get(ctx, "smtp_user")
	password, _ := s.store.Settings().Get(ctx, "smtp_password")
	from, _ := s.store.Settings().Get(ctx, "smtp_from")
	enabled, _ := s.store.Settings().Get(ctx, "smtp_enabled")

	return &SMTPConfig{
		Host:     host,
		Port:     port,
		User:     user,
		Password: password,
		From:     from,
		Enabled:  enabled == "true",
	}, nil
}

// CreateDNSSecret creates/updates a secret in the cluster under the external-dns
// namespace and returns a reference string that can be stored as dns_api_key_ref.
func (s *SettingService) CreateDNSSecret(ctx context.Context, name string, data map[string]string) (string, error) {
	// Convert to bytes
	d := make(map[string][]byte)
	for k, v := range data {
		d[k] = []byte(v)
	}
	if err := s.orch.CreateOrUpdateSecret(ctx, "external-dns", name, d); err != nil {
		return "", err
	}
	// Return a simple ref (namespace/name)
	return "external-dns/" + name, nil
}

// SaveSMTPConfig writes SMTP settings to the settings table.
func (s *SettingService) SaveSMTPConfig(ctx context.Context, cfg *SMTPConfig) error {
	// Validate required fields when enabling
	if cfg.Enabled {
		if cfg.Host == "" {
			return fmt.Errorf("SMTP host is required")
		}
		if cfg.Port == "" {
			return fmt.Errorf("SMTP port is required")
		}
		if cfg.From == "" {
			return fmt.Errorf("SMTP from address is required")
		}
	}

	// If password is the masked placeholder, keep existing password
	if cfg.Password == "••••••••" {
		existing, err := s.GetSMTPConfig(ctx)
		if err == nil && existing.Password != "" {
			cfg.Password = existing.Password
		} else {
			cfg.Password = ""
		}
	}

	pairs := map[string]string{
		"smtp_host":     cfg.Host,
		"smtp_port":     cfg.Port,
		"smtp_user":     cfg.User,
		"smtp_password": cfg.Password,
		"smtp_from":     cfg.From,
	}
	if cfg.Enabled {
		pairs["smtp_enabled"] = "true"
	} else {
		pairs["smtp_enabled"] = "false"
	}
	for k, v := range pairs {
		if err := s.store.Settings().Set(ctx, k, v); err != nil {
			return err
		}
	}
	s.logger.Info("SMTP config updated")
	return nil
}

// detectLocalIP finds the primary non-loopback IPv4 address.
func detectLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				ip := ipnet.IP.String()
				if !strings.HasPrefix(ip, "172.") && !strings.HasPrefix(ip, "169.254.") {
					return ip
				}
			}
		}
	}
	return "127.0.0.1"
}

func isValidDNSProvider(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return false
	}
	allowed := []string{"cloudflare", "route53", "digitalocean", "coredns", "pihole", "manual"}
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}
