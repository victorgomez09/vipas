#!/bin/sh
set -e

# ── Configuration ───────────────────────────────────────────────
VIPAS_VERSION="${VIPAS_VERSION:-latest}"
INSTALL_DIR="/opt/vipas"
COMPOSE_FILE="$INSTALL_DIR/docker-compose.yml"
ENV_FILE="$INSTALL_DIR/.env"

# ── Colors ──────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

info()  { printf "${CYAN}[info]${NC}  %s\n" "$1"; }
ok()    { printf "${GREEN}[ok]${NC}    %s\n" "$1"; }
warn()  { printf "${YELLOW}[warn]${NC}  %s\n" "$1"; }
fail()  { printf "${RED}[error]${NC} %s\n" "$1"; exit 1; }

# ── Preflight ───────────────────────────────────────────────────
preflight() {
    if [ "$(id -u)" -ne 0 ]; then
        fail "Please run as root: curl -sSL https://get.vipas.dev | sudo sh"
    fi

    case "$(uname -s)" in
        Linux) ;;
        *) fail "Vipas requires Linux. Detected: $(uname -s)" ;;
    esac

    ARCH="$(uname -m)"
    case "$ARCH" in
        x86_64|amd64) ARCH="amd64" ;;
        aarch64|arm64) ARCH="arm64" ;;
        *) fail "Unsupported architecture: $ARCH" ;;
    esac

    MEM_KB=$(grep MemTotal /proc/meminfo | awk '{print $2}')
    MEM_MB=$((MEM_KB / 1024))
    if [ "$MEM_MB" -lt 1800 ]; then
        warn "Low memory: ${MEM_MB}MB (recommended 2048MB+)"
    fi

    # Port check — skip if K3s already installed (re-run safe)
    if ! command -v k3s >/dev/null 2>&1; then
        for port in 80 443; do
                if ss -tlnp 2>/dev/null | grep -q ":${port} " || \
                   netstat -tlnp 2>/dev/null | grep -q ":${port} "; then
                    fail "Port ${port} is in use. Gateway requires ports 80/443 for ingress." 
                fi
            done
    fi

    command -v curl >/dev/null 2>&1 || fail "curl is required"

    ok "Preflight passed (${ARCH}, ${MEM_MB}MB RAM)"
}

# ── Install Docker ──────────────────────────────────────────────
install_docker() {
    if command -v docker >/dev/null 2>&1; then
        ok "Docker already installed"
        return
    fi

    info "Installing Docker (this may take a minute)..."
    curl -fsSL https://get.docker.com | sh >/dev/null 2>&1
    systemctl enable --now docker >/dev/null 2>&1
    ok "Docker installed"
}

# ── Install K3s ─────────────────────────────────────────────────
install_k3s() {
    if command -v k3s >/dev/null 2>&1; then
        ok "K3s already installed"
        return
    fi

    # For production we disable the embedded Traefik and Flannel in K3s
    FLANNEL_BACKEND="none"

    info "Installing K3s (Traefik and Flannel disabled)..."
    curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server \
        --disable=traefik \
        --flannel-backend=$FLANNEL_BACKEND \
        --disable-network-policy \
        --write-kubeconfig-mode=644" sh -

    info "Waiting for K3s..."
    for i in $(seq 1 60); do
        if k3s kubectl get nodes >/dev/null 2>&1; then break; fi
        sleep 2
    done
    k3s kubectl get nodes >/dev/null 2>&1 || fail "K3s failed to start"
    ok "K3s running"
}

# ── Install Helm (if missing) ───────────────────────────────────
install_helm() {
    if command -v helm >/dev/null 2>&1; then
        ok "Helm already installed"
        return
    fi

    info "Installing Helm client..."
    curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash >/dev/null 2>&1 || \
        fail "Failed to install helm"
    ok "Helm installed"
}

# ── Install Cilium via Helm (production-ready defaults) ──────────
install_cilium() {
    if k3s kubectl -n kube-system get daemonset cilium >/dev/null 2>&1; then
        ok "Cilium already installed in cluster"
        return
    fi

    install_helm

    . "$ENV_FILE" 2>/dev/null || true

    # Derive API server address for Cilium to talk to the apiserver
    K8S_SERVICE_HOST="${K8S_SERVICE_HOST:-${SERVER_IP:-127.0.0.1}}"
    K8S_SERVICE_PORT="${K8S_SERVICE_PORT:-6443}"

    # Detect WireGuard support (kernel/module present)
    ENCRYPTION=false
    if command -v modprobe >/dev/null 2>&1 && modprobe --dry-run wireguard >/dev/null 2>&1; then
        ENCRYPTION=true
        ok "WireGuard appears supported — enabling Cilium encryption"
    else
        warn "WireGuard not detected — Cilium encryption will remain disabled"
    fi

    # Add Cilium Helm repo and update
    info "Adding Cilium Helm repo"
    helm repo add cilium https://helm.cilium.io >/dev/null 2>&1 || true
    helm repo update >/dev/null 2>&1 || true

    # Pin a tested Cilium version for production (update as needed)
    CILIUM_HELM_VERSION="v1.14.0"

    info "Installing Cilium (this may take a few minutes)..."
    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

    helm upgrade --install cilium cilium/cilium \
      --namespace kube-system \
      --create-namespace \
      --version ${CILIUM_HELM_VERSION} \
      --wait \
      --timeout 10m \
      --set kubeProxyReplacement=strict \
      --set k8sServiceHost=${K8S_SERVICE_HOST} \
      --set k8sServicePort=${K8S_SERVICE_PORT} \
      --set hubble.relay.enabled=true \
      --set hubble.ui.enabled=true \
      --set encryption.enabled=${ENCRYPTION} >/dev/null 2>&1 || warn "Helm install/upgrade returned non-zero (check logs)"

    # Validation: prefer `cilium status --wait` if cilium CLI is present
    if command -v cilium >/dev/null 2>&1; then
        info "Waiting for Cilium components via cilium CLI..."
        if ! cilium status --wait 120s >/dev/null 2>&1; then
            warn "cilium status reported problems — check: cilium status"
        else
            ok "Cilium components healthy"
        fi
    else
        info "Waiting for Cilium pods to be ready via kubectl..."
        if ! k3s kubectl -n kube-system wait --for=condition=Available deployment/cilium-operator --timeout=300s >/dev/null 2>&1; then
            warn "Timed out waiting for Cilium operator — check: k3s kubectl -n kube-system get pods"
        else
            ok "Cilium operator available"
        fi
    fi
}

# Traefik is no longer managed by the installer. TLS and ingress
# are handled by cert-manager + Envoy Gateway after Cilium is installed.

# ── Generate secrets ────────────────────────────────────────────
generate_secrets() {
    if [ -f "$ENV_FILE" ]; then
        ok "Configuration exists: $ENV_FILE"
        # Backfill required keys that older installs may lack
        if ! grep -q '^SETUP_SECRET=' "$ENV_FILE"; then
            SETUP_SECRET=$(head -c 32 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 32)
            echo "SETUP_SECRET=$SETUP_SECRET" >> "$ENV_FILE"
            ok "Generated missing SETUP_SECRET"
        fi
        return
    fi

    DB_PASSWORD=$(head -c 32 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 32)
    JWT_SECRET=$(head -c 48 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 48)
    SETUP_SECRET=$(head -c 32 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 32)

    # Detect IP — prefer private/LAN for internal use, public for APP_URL
    SERVER_IP=$(curl -sf --max-time 5 https://api.ipify.org 2>/dev/null || \
                curl -sf --max-time 5 https://ifconfig.me 2>/dev/null || \
                hostname -I | awk '{print $1}')

    mkdir -p "$INSTALL_DIR"
    cat > "$ENV_FILE" <<EOF
DB_PASSWORD=$DB_PASSWORD
JWT_SECRET=$JWT_SECRET
SETUP_SECRET=$SETUP_SECRET
SERVER_IP=$SERVER_IP
VIPAS_VERSION=latest
EOF
    chmod 600 "$ENV_FILE"
    ok "Secrets generated"
    warn "APP_URL set to http://$SERVER_IP:3000 — change in Settings if behind NAT/proxy"
}

# ── Deploy via Docker Compose ───────────────────────────────────
deploy() {
    mkdir -p "$INSTALL_DIR"

    . "$ENV_FILE"

    cat > "$COMPOSE_FILE" <<COMPOSEFILE
services:
  vipas:
    image: ghcr.io/victorgomez09/vipas:\${VIPAS_VERSION}
    container_name: vipas
    restart: unless-stopped
    network_mode: host
    environment:
      DATABASE_URL: postgres://vipas:\${DB_PASSWORD}@127.0.0.1:54321/vipas?sslmode=disable
      JWT_SECRET: \${JWT_SECRET}
      SETUP_SECRET: \${SETUP_SECRET}
      K8S_IN_CLUSTER: "false"
      KUBECONFIG: /etc/rancher/k3s/k3s.yaml
      APP_URL: http://${SERVER_IP}:3000
      SERVER_PORT: "8080"
    volumes:
      - /etc/rancher/k3s/k3s.yaml:/etc/rancher/k3s/k3s.yaml:ro
      - /var/run/docker.sock:/var/run/docker.sock
      - ${INSTALL_DIR}:/opt/vipas

  postgres:
    image: postgres:18-alpine
    container_name: vipas-postgres
    restart: unless-stopped
    ports:
      - "127.0.0.1:54321:5432"
    environment:
      POSTGRES_DB: vipas
      POSTGRES_USER: vipas
      POSTGRES_PASSWORD: \${DB_PASSWORD}
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U vipas"]
      interval: 5s
      timeout: 3s
      retries: 10

volumes:
  pgdata:
COMPOSEFILE

    # Download upgrade library for self-upgrade support
    LIB_URL="https://raw.githubusercontent.com/victorgomez09/vipas/main/deploy/upgrade-lib.sh"
    if curl -sSL --max-time 10 "$LIB_URL" -o "$INSTALL_DIR/upgrade-lib.sh" 2>/dev/null && [ -s "$INSTALL_DIR/upgrade-lib.sh" ]; then
        chmod +x "$INSTALL_DIR/upgrade-lib.sh"
        ok "Upgrade library installed"
    else
        warn "Could not download upgrade library — self-upgrade from panel will not be available"
    fi

    info "Pulling images..."
    if ! docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" pull 2>&1; then
        # If pull fails, check if image exists locally (pre-loaded)
        if docker image inspect "ghcr.io/victorgomez09/vipas:${VIPAS_VERSION}" >/dev/null 2>&1; then
            warn "Pull failed but local image found — using it"
        else
            fail "Failed to pull ghcr.io/victorgomez09/vipas:${VIPAS_VERSION}. Check your internet connection."
        fi
    fi

    # Start PG first, wait for healthy, then start Vipas
    info "Starting PostgreSQL..."
    docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d postgres
    for i in $(seq 1 30); do
        if docker exec vipas-postgres pg_isready -U vipas >/dev/null 2>&1; then break; fi
        sleep 2
    done
    docker exec vipas-postgres pg_isready -U vipas >/dev/null 2>&1 || fail "PostgreSQL failed to start"
    ok "PostgreSQL ready"

    info "Starting Vipas..."
    docker compose -f "$COMPOSE_FILE" --env-file "$ENV_FILE" up -d vipas

    info "Waiting for Vipas to be ready..."
    for i in $(seq 1 90); do
        if curl -sf http://localhost:3000/healthz >/dev/null 2>&1; then
            ok "Vipas is running"
            return
        fi
        sleep 2
    done

    fail "Vipas failed to start after 180s. Check: docker compose -f $COMPOSE_FILE logs"
}

# ── Summary ─────────────────────────────────────────────────────
summary() {
    . "$ENV_FILE"

    printf "\n"
    printf "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
    printf "${GREEN}  Vipas is ready!${NC}\n"
    printf "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}\n"
    printf "\n"
    printf "  ${BOLD}Panel:${NC}    ${CYAN}http://%s:3000${NC}\n" "$SERVER_IP"
    printf "  ${BOLD}Config:${NC}   %s\n" "$ENV_FILE"
    printf "  ${BOLD}Logs:${NC}     docker compose -f %s logs -f\n" "$COMPOSE_FILE"
    printf "  ${BOLD}Upgrade:${NC}  docker compose -f %s pull && docker compose -f %s up -d\n" "$COMPOSE_FILE" "$COMPOSE_FILE"
    printf "\n"
    printf "  ${BOLD}Port usage:${NC}\n"
    printf "    :3000  → Vipas panel\n"
    printf "    :80    → Gateway HTTP  (your deployed apps)\n"
    printf "    :443   → Gateway HTTPS (your deployed apps)\n"
    printf "    :6443  → K3s API\n"
    printf "\n"
    printf "  Open the panel in your browser to create your admin account.\n"
    printf "\n"
}

# ── Main ────────────────────────────────────────────────────────
main() {
    printf "\n"
    printf "${CYAN}  ⛵ Vipas Installer${NC}\n"
    printf "${CYAN}  Self-hosted PaaS, powered by Kubernetes${NC}\n"
    printf "\n"

    preflight
    install_docker
    install_k3s
    install_cilium
    generate_secrets
    deploy
    summary
}

main "$@"
