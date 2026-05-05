package model

import (
	"time"

	"github.com/google/uuid"
)

// Domain maps a custom domain to an application.
type Domain struct {
	BaseModel `bun:"table:domains,alias:dom"`

	AppID       uuid.UUID    `bun:"app_id,notnull,type:uuid" json:"app_id"`
	Application *Application `bun:"rel:belongs-to,join:app_id=id" json:"-"`

	Host     string `bun:"host,notnull" json:"host"`
	TLS      bool   `bun:"tls" json:"tls"`
	AutoCert bool   `bun:"auto_cert" json:"auto_cert"`

	ForceHTTPS bool       `bun:"force_https" json:"force_https"`
	CertExpiry *time.Time `bun:"cert_expiry" json:"cert_expiry,omitempty"`
	RouteReady bool       `bun:"route_ready" json:"route_ready"`

	// AutoDNS indicates that an external-dns provider is configured and the
	// platform will attempt to create the A record automatically. This field
	// is transient (not stored in DB).
	AutoDNS bool `bun:"-" json:"auto_dns,omitempty"`
}
