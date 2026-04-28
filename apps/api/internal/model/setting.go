package model

import "time"

// Setting stores global key-value configuration in the database.
type Setting struct {
	Key       string    `bun:"key,pk" json:"key"`
	Value     string    `bun:"value,notnull" json:"value"`
	UpdatedAt time.Time `bun:"updated_at,notnull,default:current_timestamp" json:"updated_at"`
}

// Well-known setting keys
const (
	SettingBaseDomain  = "base_domain"  // e.g. "203.0.113.5.sslip.io" or "mysite.com"
	SettingServerIP    = "server_ip"    // auto-detected server IP
	SettingSetupDone   = "setup_done"   // "true" after first-time setup
	SettingPanelDomain = "panel_domain" // domain for the Vipas panel (e.g. "panel.example.com")
	SettingHTTPSEmail  = "https_email"  // email for Let's Encrypt ACME certificates
	SettingCertIssuer  = "cert_issuer"  // letsencrypt-staging | letsencrypt-prod | selfsigned
	SettingLBType      = "lb_type"      // cilium-l2 | nodeport
	SettingLBIPPool    = "lb_ip_pool"   // CIDR for CiliumLoadBalancerIPPool
	SettingGatewayIP   = "gateway_ip"   // external IP assigned to the Envoy Gateway Service
	SettingK3sAPIVIP   = "k3s_api_vip"  // VIP for K3s API server in HA mode (e.g. 10.0.0.10)

	// DNS / external-dns settings
	SettingDNSProvider = "dns_provider" // cloudflare | route53 | digitalocean | coredns | pihole | manual
	// Note: API keys should be stored securely (External Secrets / Vault). This key is a reference name.
	SettingDNSAPIKeyRef = "dns_api_key_ref" // reference to encrypted secret holding DNS API key
	SettingDNSZone      = "dns_zone"        // DNS zone where records will be created (e.g. example.com)
)
