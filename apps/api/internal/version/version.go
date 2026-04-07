package version

// Version is injected at build time. Defaults to "dev" for local development.
// Production builds set this via: go build -ldflags "-X .../version.Version=v1.0.0"
var Version = "dev"

const (
	// Brand
	Name    = "Vipas"
	Website = "https://github.com/victorgomez09/vipas"
	License = "AGPL-3.0"

	// GitHub
	GitHubOwner = "victorgomez09"
	GitHubRepo  = "vipas"
)
