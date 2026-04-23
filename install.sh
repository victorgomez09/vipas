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

    # Low-memory hint mode
    LOW_MEMORY_MODE="false"
    if [ "$MEM_MB" -lt 2048 ]; then
        warn "System has <2GB RAM — enabling low-memory defaults"
        LOW_MEMORY_MODE="true"
    fi

    # Suggest Raspberry/low-memory mode when running on ARM with limited RAM
    RPI_MODE="false"
    if [ "$ARCH" = "arm64" ] && [ "$MEM_MB" -lt 4096 ]; then
        warn "Detected ARM64 with <4GB RAM — enabling Raspberry/low-memory defaults"
        RPI_MODE="true"
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

    . "$ENV_FILE" 2>/dev/null || true

    K3S_HA_MODE="${K3S_HA_MODE:-false}"
    K3S_CLUSTER_INIT="${K3S_CLUSTER_INIT:-false}"
    K3S_API_VIP="${K3S_API_VIP:-}"
    K3S_SERVER_URL="${K3S_SERVER_URL:-}"

    if [ -z "$K3S_SERVER_URL" ] && [ -n "$K3S_API_VIP" ]; then
        K3S_SERVER_URL="https://${K3S_API_VIP}:6443"
    fi

    BASE_EXEC="server --disable=traefik --flannel-backend=$FLANNEL_BACKEND --disable-network-policy --write-kubeconfig-mode=644"
    INSTALL_EXEC="$BASE_EXEC"

    if [ "$K3S_HA_MODE" = "true" ]; then
        if [ "$K3S_CLUSTER_INIT" = "true" ]; then
            INSTALL_EXEC="$BASE_EXEC --cluster-init"
            info "Installing K3s control-plane in HA bootstrap mode (--cluster-init)..."
        else
            [ -n "$K3S_SERVER_URL" ] || fail "K3S_SERVER_URL or K3S_API_VIP is required when joining HA control-plane"
            INSTALL_EXEC="$BASE_EXEC --server $K3S_SERVER_URL"
            info "Installing K3s control-plane joining HA server: $K3S_SERVER_URL"
        fi
    else
        info "Installing K3s (Traefik and Flannel disabled)..."
    fi

    if [ "$K3S_HA_MODE" = "true" ] && [ "$K3S_CLUSTER_INIT" != "true" ]; then
        [ -n "${K3S_TOKEN:-}" ] || fail "K3S_TOKEN is required for HA control-plane join"
        curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="$INSTALL_EXEC" K3S_TOKEN="$K3S_TOKEN" sh -
    else
        curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="$INSTALL_EXEC" sh -
    fi

    info "Waiting for K3s..."
    for i in $(seq 1 60); do
        if k3s kubectl get nodes >/dev/null 2>&1; then break; fi
        sleep 2
    done
    k3s kubectl get nodes >/dev/null 2>&1 || fail "K3s failed to start"
    ok "K3s running"
}

# ── Configure kube-vip API VIP (HA control-plane) ──────────────
configure_kube_vip() {
    . "$ENV_FILE" 2>/dev/null || true

    K3S_HA_MODE="${K3S_HA_MODE:-false}"
    K3S_API_VIP="${K3S_API_VIP:-}"
    KUBE_VIP_INTERFACE="${KUBE_VIP_INTERFACE:-}"

    if [ "$K3S_HA_MODE" != "true" ]; then
        return
    fi

    if [ -z "$K3S_API_VIP" ]; then
        warn "K3S_API_VIP is not set, skipping kube-vip API VIP setup"
        return
    fi

    if [ -z "$KUBE_VIP_INTERFACE" ]; then
        KUBE_VIP_INTERFACE=$(ip route 2>/dev/null | awk '/default/ {print $5; exit}')
    fi
    [ -n "$KUBE_VIP_INTERFACE" ] || KUBE_VIP_INTERFACE="eth0"

    info "Applying kube-vip (API VIP ${K3S_API_VIP}:6443 on ${KUBE_VIP_INTERFACE})"
    TMP_KV=$(mktemp)
    sed "s|\${K3S_API_VIP}|${K3S_API_VIP}|g; s|\${KUBE_VIP_INTERFACE}|${KUBE_VIP_INTERFACE}|g" deploy/manifests/kube-vip-api-vip.yaml > "$TMP_KV"
    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
    if ! k3s kubectl apply -f "$TMP_KV" >/dev/null 2>&1; then
        warn "Failed to apply kube-vip resources"
        rm -f "$TMP_KV"
        return
    fi
    rm -f "$TMP_KV"

    if ! k3s kubectl -n kube-system rollout status daemonset/kube-vip-ds --timeout=180s >/dev/null 2>&1; then
        warn "kube-vip DaemonSet rollout did not complete in time"
    else
        ok "kube-vip API VIP configured"
    fi
}

# ── Write kubeconfig pointing to API VIP ───────────────────────
configure_kubeconfig_vip() {
    . "$ENV_FILE" 2>/dev/null || true

    K3S_API_VIP="${K3S_API_VIP:-}"
    if [ -z "$K3S_API_VIP" ]; then
        return
    fi

    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
    mkdir -p "$INSTALL_DIR/kubeconfig"
    sed "s|https://127.0.0.1:6443|https://${K3S_API_VIP}:6443|g" /etc/rancher/k3s/k3s.yaml > "$INSTALL_DIR/kubeconfig/kubeconfig.yaml"
    chmod 600 "$INSTALL_DIR/kubeconfig/kubeconfig.yaml"
    ok "Wrote kubeconfig with API VIP to $INSTALL_DIR/kubeconfig/kubeconfig.yaml"
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
    if [ -f "./deploy/versions.env" ]; then
        . ./deploy/versions.env
    fi

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

        # Allow optional extra Helm args via ENV_FILE (CILIUM_EXTRA_HELM_ARGS)
        CILIUM_EXTRA_HELM_ARGS="${CILIUM_EXTRA_HELM_ARGS:-}"
        if [ "${RPI_MODE}" = "true" ] || [ "${LOW_MEMORY_MODE:-false}" = "true" ]; then
            warn "Applying low-memory Helm settings for Cilium (disabling Hubble UI/relay)"
            CILIUM_EXTRA_HELM_ARGS="$CILIUM_EXTRA_HELM_ARGS --set hubble.relay.enabled=false --set hubble.ui.enabled=false"
            if [ -f deploy/values/cilium-arm64-overrides.yaml ]; then
                CILIUM_EXTRA_HELM_ARGS="$CILIUM_EXTRA_HELM_ARGS -f deploy/values/cilium-arm64-overrides.yaml"
            fi
        else
            CILIUM_EXTRA_HELM_ARGS="$CILIUM_EXTRA_HELM_ARGS --set hubble.relay.enabled=true --set hubble.ui.enabled=true"
        fi

        helm upgrade --install cilium cilium/cilium \
            --namespace kube-system \
            --create-namespace \
            --version ${CILIUM_HELM_VERSION} \
            --wait \
            --timeout 10m \
            --set kubeProxyReplacement=strict \
            --set k8sServiceHost=${K8S_SERVICE_HOST} \
            --set k8sServicePort=${K8S_SERVICE_PORT} \
            --set encryption.enabled=${ENCRYPTION} \
            ${CILIUM_EXTRA_HELM_ARGS} >/dev/null 2>&1 || warn "Helm install/upgrade returned non-zero (check logs)"

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


# ── Configure Cilium LB mode (dev/prod) ────────────────────────
configure_cilium_lb() {
    . "$ENV_FILE" 2>/dev/null || true

    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

    LB_TYPE="${LB_TYPE:-}"
    LB_IP_POOL="${LB_IP_POOL:-}"

    if [ -z "$LB_TYPE" ]; then
        NODE_COUNT=$(k3s kubectl get nodes --no-headers 2>/dev/null | wc -l | tr -d '[:space:]')
        if [ "${NODE_COUNT:-0}" -le 1 ]; then
            LB_TYPE="cilium-l2"
        else
            LB_TYPE="cilium-bgp"
        fi
        info "LB_TYPE not set, inferred ${LB_TYPE} from topology (${NODE_COUNT} node/s)"
    fi

    case "$LB_TYPE" in
        cilium-l2|cilium-bgp|nodeport) ;;
        *)
            warn "Unsupported LB_TYPE=${LB_TYPE}. Allowed: cilium-l2 | cilium-bgp | nodeport"
            return
            ;;
    esac

    if [ "$LB_TYPE" = "nodeport" ]; then
        info "LB_TYPE=nodeport, skipping Cilium LB resources"
        return
    fi

    if [ -z "$LB_IP_POOL" ]; then
        warn "LB_IP_POOL not set — skipping LB pool creation. Configure LB_IP_POOL in $ENV_FILE"
        return
    fi

    info "Applying Cilium LB pool: ${LB_IP_POOL}"
    TMP_POOL=$(mktemp)
    sed "s|\${LB_IP_POOL}|${LB_IP_POOL}|g" deploy/manifests/cilium-lb-ip-pool.yaml > "$TMP_POOL"
    if ! k3s kubectl apply -f "$TMP_POOL" >/dev/null 2>&1; then
        warn "Failed to apply CiliumLoadBalancerIPPool"
        rm -f "$TMP_POOL"
        return
    fi
    rm -f "$TMP_POOL"
    ok "CiliumLoadBalancerIPPool applied"

    if [ "$LB_TYPE" = "cilium-l2" ]; then
        info "Applying Cilium L2 announcement policy"
        if ! k3s kubectl apply -f deploy/manifests/cilium-l2-announcement.yaml >/dev/null 2>&1; then
            warn "Failed to apply CiliumL2AnnouncementPolicy"
        else
            ok "Cilium L2 announcement configured"
        fi
        k3s kubectl delete ciliumbgppeeringpolicy vipas-bgp-peering --ignore-not-found >/dev/null 2>&1 || true
        return
    fi

    # cilium-bgp mode
    BGP_LOCAL_ASN="${BGP_LOCAL_ASN:-64512}"
    BGP_PEER_ADDRESS="${BGP_PEER_ADDRESS:-}"
    BGP_PEER_ASN="${BGP_PEER_ASN:-}"

    if [ -z "$BGP_PEER_ADDRESS" ] || [ -z "$BGP_PEER_ASN" ]; then
        warn "BGP mode selected but BGP_PEER_ADDRESS/BGP_PEER_ASN not set. Pool created; peer setup remains pending."
        return
    fi

    info "Applying Cilium BGP peering policy (${BGP_PEER_ADDRESS}, ASN ${BGP_PEER_ASN})"
    TMP_BGP=$(mktemp)
    sed "s|\${BGP_LOCAL_ASN}|${BGP_LOCAL_ASN}|g; s|\${BGP_PEER_ADDRESS}|${BGP_PEER_ADDRESS}|g; s|\${BGP_PEER_ASN}|${BGP_PEER_ASN}|g" deploy/manifests/cilium-bgp-peering-policy.yaml > "$TMP_BGP"
    if ! k3s kubectl apply -f "$TMP_BGP" >/dev/null 2>&1; then
        warn "Failed to apply CiliumBGPPeeringPolicy"
    else
        ok "Cilium BGP peering policy applied"
    fi
    rm -f "$TMP_BGP"
    k3s kubectl delete ciliuml2announcementpolicy vipas-l2-announcement --ignore-not-found >/dev/null 2>&1 || true
}

# ── Install Gateway API CRDs ────────────────────────────────────
install_gateway_api_crds() {
    info "Installing Gateway API CRDs"
    # prefer pinned version if available
    if [ -f "./deploy/versions.env" ]; then
        . ./deploy/versions.env
    fi
    GATEWAY_API_URL="https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/standard-install.yaml"
    info "Applying Gateway API CRDs: ${GATEWAY_API_URL}"
    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
    if ! k3s kubectl apply -f "${GATEWAY_API_URL}" >/dev/null 2>&1; then
        warn "Failed to apply Gateway API CRDs (check network)."
    else
        ok "Gateway API CRDs applied"
    fi
}


# ── Install Envoy Gateway via Helm ──────────────────────────────
install_envoy_gateway() {
    info "Installing Envoy Gateway"
    if [ -f "./deploy/versions.env" ]; then
        . ./deploy/versions.env
    fi
    install_helm
    helm registry login docker.io >/dev/null 2>&1 || true
    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
        # Allow extra helm args/overrides via ENV_FILE: ENVOY_EXTRA_HELM_ARGS
        ENVOY_EXTRA_HELM_ARGS="${ENVOY_EXTRA_HELM_ARGS:-}"
        if [ "${ARCH}" = "arm64" ] || [ "${RPI_MODE}" = "true" ]; then
            warn "Running on ARM64/RPI mode. Ensure Envoy Gateway images are available for your architecture or set ENVOY_EXTRA_HELM_ARGS to override image repository/tag."
            if [ -f deploy/values/envoy-arm64-overrides.yaml ]; then
                ENVOY_EXTRA_HELM_ARGS="$ENVOY_EXTRA_HELM_ARGS -f deploy/values/envoy-arm64-overrides.yaml"
            fi
        fi
        helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm \
          --version ${ENVOY_GATEWAY_VERSION} \
          -n envoy-gateway-system --create-namespace \
          --wait --timeout 5m \
          ${ENVOY_EXTRA_HELM_ARGS} >/dev/null 2>&1 || warn "Envoy Gateway helm install returned non-zero"
    ok "Envoy Gateway install invoked"
}


# ── Apply Gateway manifests (GatewayClass + Gateway) ────────────
apply_gateway_manifests() {
    info "Applying Gateway manifests"
    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
    k3s kubectl create ns gateway-system >/dev/null 2>&1 || true

    # Dedicated gateway-node mode uses an Envoy DaemonSet pinned to vipas/pool=gateway.
    # Default mode keeps a regular Envoy Deployment suitable for single-node/dev.
    if [ "${GATEWAY_DEDICATED_NODES:-false}" = "true" ]; then
        k3s kubectl apply -f deploy/manifests/envoyproxy-gateway-nodes.yaml >/dev/null 2>&1 || warn "Failed to apply envoyproxy-gateway-nodes.yaml"
    else
        k3s kubectl apply -f deploy/manifests/envoyproxy.yaml >/dev/null 2>&1 || warn "Failed to apply envoyproxy.yaml"
    fi

    k3s kubectl apply -f deploy/manifests/gatewayclass.yaml >/dev/null 2>&1 || warn "Failed to apply gatewayclass.yaml"
    k3s kubectl apply -f deploy/manifests/gateway.yaml >/dev/null 2>&1 || warn "Failed to apply gateway.yaml"

    info "Waiting for Gateway to become Accepted and Programmed"
    for i in $(seq 1 60); do
        ACC=$(k3s kubectl -n gateway-system get gateway vipas-gateway -o jsonpath='{.status.conditions[?(@.type=="Accepted")].status}' 2>/dev/null || echo "")
        PRG=$(k3s kubectl -n gateway-system get gateway vipas-gateway -o jsonpath='{.status.conditions[?(@.type=="Programmed")].status}' 2>/dev/null || echo "")
        if [ "$ACC" = "True" ] && [ "$PRG" = "True" ]; then
            ok "Gateway accepted and programmed"
            return
        fi
        sleep 2
    done
    warn "Gateway did not reach Accepted/Programmed in time"
}


# ── Install cert-manager via Helm and create staging issuer ─────
install_cert_manager() {
    info "Installing cert-manager"
    if [ -f "./deploy/versions.env" ]; then
        . ./deploy/versions.env
    fi
    install_helm
    helm repo add jetstack https://charts.jetstack.io >/dev/null 2>&1 || true
    helm repo update >/dev/null 2>&1 || true
    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
    helm upgrade --install cert-manager jetstack/cert-manager \
      -n cert-manager --create-namespace \
      --version ${CERT_MANAGER_VERSION} \
      --wait --timeout 5m \
      --set installCRDs=true >/dev/null 2>&1 || warn "cert-manager helm install returned non-zero"

    # Wait for cert-manager deployment
    if ! k3s kubectl -n cert-manager rollout status deploy/cert-manager --timeout=180s >/dev/null 2>&1; then
        warn "cert-manager deployment rollout timed out"
    else
        ok "cert-manager deployed"
    fi

    # Create staging ClusterIssuer by default (admin may switch to prod later)
    if [ -f deploy/manifests/clusterissuer-staging.yaml ]; then
        k3s kubectl apply -f deploy/manifests/clusterissuer-staging.yaml >/dev/null 2>&1 || warn "Failed to apply clusterissuer-staging.yaml"
        ok "ClusterIssuer 'letsencrypt-staging' applied (email: admin@example.com)"
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
DNS_PROVIDER=coredns
DNS_ZONE=
EOF
    chmod 600 "$ENV_FILE"
    ok "Secrets generated"
    warn "APP_URL set to http://$SERVER_IP:3000 — change in Settings if behind NAT/proxy"
}


# ── Install external-dns (optional) ─────────────────────────────
install_external_dns() {
    . "$ENV_FILE" 2>/dev/null || true
    DNS_PROVIDER="${DNS_PROVIDER:-coredns}"
    DNS_ZONE="${DNS_ZONE:-}"

    if [ "$DNS_PROVIDER" = "manual" ] || [ -z "$DNS_PROVIDER" ]; then
        info "DNS_PROVIDER is set to manual or empty — skipping external-dns install"
        return
    fi

    install_helm
    info "Installing external-dns (provider=${DNS_PROVIDER})"
    helm repo add external-dns https://kubernetes-sigs.github.io/external-dns/ >/dev/null 2>&1 || true
    helm repo update >/dev/null 2>&1 || true
    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml

    EXTRA_ARGS="--set provider=${DNS_PROVIDER} --set source=gateway-httproute --set txtOwnerId=vipas"
    if [ -n "$DNS_ZONE" ]; then
        EXTRA_ARGS="$EXTRA_ARGS --set domainFilters[0]=${DNS_ZONE}"
    fi

    if ! helm upgrade --install external-dns external-dns/external-dns \
        --version ${EXTERNAL_DNS_VERSION:-} \
        -n external-dns --create-namespace --wait --timeout 3m $EXTRA_ARGS >/dev/null 2>&1; then
        warn "external-dns helm install returned non-zero — check logs"
    else
        ok "external-dns install invoked"
    fi
}

# ── Deploy via Docker Compose ───────────────────────────────────
deploy() {
    mkdir -p "$INSTALL_DIR"

    . "$ENV_FILE"

        cat > "$COMPOSE_FILE" <<COMPOSEFILE
services:
    vipas:
        image: \\${VIPAS_IMAGE:-ghcr.io/victorgomez09/vipas:\\${VIPAS_VERSION}}
        container_name: vipas
        restart: unless-stopped
        network_mode: host
        environment:
            DATABASE_URL: postgres://vipas:\\${DB_PASSWORD}@127.0.0.1:54321/vipas?sslmode=disable
            JWT_SECRET: \\${JWT_SECRET}
            SETUP_SECRET: \\${SETUP_SECRET}
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
            POSTGRES_PASSWORD: \\${DB_PASSWORD}
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
        IMAGE_TO_CHECK="${VIPAS_IMAGE:-ghcr.io/victorgomez09/vipas:${VIPAS_VERSION}}"
        if docker image inspect "$IMAGE_TO_CHECK" >/dev/null 2>&1; then
            warn "Pull failed but local image found — using it ($IMAGE_TO_CHECK)"
        else
            fail "Failed to pull $IMAGE_TO_CHECK. Check your internet connection."
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
    configure_kube_vip
    configure_kubeconfig_vip
    install_cilium
    configure_cilium_lb
    install_gateway_api_crds
    install_envoy_gateway
    apply_gateway_manifests
    install_cert_manager
    install_external_dns
    generate_secrets
    deploy
    summary
}

main "$@"
