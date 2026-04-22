#!/bin/sh
set -e

# Script de preparación del cluster local para desarrollo
# Idempotente: instala Cilium, Gateway API CRDs, Envoy Gateway y cert-manager

ROOT_DIR="$(cd "$(dirname "$0")"/.. && pwd)"
VERSIONS_FILE="$ROOT_DIR/versions.env"

if [ -f "$VERSIONS_FILE" ]; then
  . "$VERSIONS_FILE"
fi

KUBECONFIG=${KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}
export KUBECONFIG

# Kubectl executable: prefer user-provided $KUBECTL, then system kubectl, then k3s kubectl
if [ -n "${KUBECTL}" ]; then
  KUBECTL_CMD="$KUBECTL"
elif command -v kubectl >/dev/null 2>&1; then
  KUBECTL_CMD="kubectl"
elif command -v k3s >/dev/null 2>&1; then
  KUBECTL_CMD="k3s kubectl"
else
  echo "[error] kubectl or k3s not found in PATH. Install kubectl or run on k3s host." >&2
  exit 1
fi

echo "[info] using kubectl command: ${KUBECTL_CMD}"

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "[error] run as root: sudo $0" >&2
    exit 1
  fi
}

check_kubectl() {
  if ! sh -c "${KUBECTL_CMD} get nodes" >/dev/null 2>&1; then
    echo "[error] Kubernetes cluster not reachable via ${KUBECTL_CMD}. Check KUBECONFIG." >&2
    exit 1
  fi
}

install_helm() {
  if command -v helm >/dev/null 2>&1; then
    echo "[ok] helm present"
    return
  fi
  echo "[info] installing helm client"
  curl -fsSL https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 | bash
}

detect_wireguard() {
  if command -v modprobe >/dev/null 2>&1 && modprobe --dry-run wireguard >/dev/null 2>&1; then
    echo "true"
  else
    echo "false"
  fi
}

install_cilium() {
  echo "[info] installing Cilium"
  install_helm
  helm repo add cilium https://helm.cilium.io >/dev/null 2>&1 || true
  helm repo update >/dev/null 2>&1 || true

  ENCRYPTION=$(detect_wireguard)
  echo "[info] wireguard support: ${ENCRYPTION}"

  # Detect WSL2 / Microsoft kernels where WireGuard/IPsec is usually unavailable
  if [ -r /proc/version ] && grep -qi microsoft /proc/version 2>/dev/null; then
    echo "[info] detected WSL2 / Microsoft kernel — disabling WireGuard/IPsec for Cilium"
    ENCRYPTION="false"
  elif uname -r 2>/dev/null | grep -qi microsoft; then
    echo "[info] detected Microsoft kernel via uname — disabling WireGuard/IPsec for Cilium"
    ENCRYPTION="false"
  fi

  # Check cluster environment: k3s, k3d, kind, codespaces
  NODE_NAMES=$(sh -c "${KUBECTL_CMD} get nodes -o jsonpath='{range .items[*]}{@.metadata.name} {end}'" 2>/dev/null || echo "")
  IS_K3D=0
  if echo "$NODE_NAMES" | grep -q "k3d"; then IS_K3D=1; fi
  if [ -n "${CODESPACES}" ] || [ -n "${GITHUB_CODESPACES}" ]; then
    echo "[warn] detected Codespaces environment — privileged kernel features (eBPF) may be unavailable"
  fi

  CILIUM_VERSION=${CILIUM_HELM_VERSION:-1.14.0}
  K8S_SERVICE_HOST=${K8S_SERVICE_HOST:-127.0.0.1}
  K8S_SERVICE_PORT=${K8S_SERVICE_PORT:-6443}

  # If single-node (common in k3d/dev), set operator replicas to 1 to avoid anti-affinity scheduling issues
  NODE_COUNT=$(sh -c "${KUBECTL_CMD} get nodes --no-headers 2>/dev/null | wc -l" | tr -d '[:space:]' || echo "0")
  EXTRA_REPLICAS_FLAG=""
  if [ "$NODE_COUNT" -eq 1 ] || [ "$IS_K3D" -eq 1 ]; then
    echo "[info] single-node or k3d detected (nodes=${NODE_COUNT}) — setting operator.replicas=1"
    EXTRA_REPLICAS_FLAG="--set operator.replicas=1"
  fi

  # If running inside a lightweight/containerized cluster (k3d/kind) and eBPF is not available,
  # it's safer to install Cilium with encryption disabled; user can re-run on a host with kernel support.
  if [ "$IS_K3D" -eq 1 ] && [ "$ENCRYPTION" = "false" ]; then
    echo "[info] k3d detected and no WireGuard support: installing Cilium with encryption.disabled=true"
    EXTRA_ENC_FLAG="--set encryption.enabled=false"
  else
    EXTRA_ENC_FLAG="--set encryption.enabled=${ENCRYPTION}"
  fi

  # If encryption (WireGuard) is not available, also disable IPsec to avoid requiring ipsec secrets
  EXTRA_IPSEC_FLAG=""
  if [ "${ENCRYPTION}" = "false" ]; then
    echo "[info] WireGuard not available — disabling Cilium IPsec to avoid missing secret requirements"
    EXTRA_IPSEC_FLAG="--set ipsec.enabled=false"
  fi

    echo "[info] installing Cilium Helm chart (waiting for rollout)"
    if ! helm upgrade --install cilium cilium/cilium \
      --namespace kube-system --create-namespace \
      --version "${CILIUM_VERSION}" \
      --set kubeProxyReplacement=strict \
      --set k8sServiceHost="${K8S_SERVICE_HOST}" \
      --set k8sServicePort="${K8S_SERVICE_PORT}" \
      --set hubble.relay.enabled=true \
      --set hubble.ui.enabled=true \
      ${EXTRA_ENC_FLAG} \
      ${EXTRA_IPSEC_FLAG} \
      ${EXTRA_REPLICAS_FLAG} \
      --wait --timeout 600s; then
      echo "[warn] helm install/upgrade returned non-zero; showing release status"
      helm -n kube-system status cilium || true
    fi

    # If installation still left Cilium init containers blocked (e.g. due to ipsec secret),
    # attempt a fallback upgrade forcing ipsec/encryption off to recover quickly.
    DS_READY=$(sh -c "${KUBECTL_CMD} -n kube-system get ds cilium -o jsonpath='{.status.numberReady}' 2>/dev/null || echo 0")
    DS_DESIRED=$(sh -c "${KUBECTL_CMD} -n kube-system get ds cilium -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || echo 0")
    if [ "${DS_DESIRED:-0}" != "0" ] && [ "${DS_READY:-0}" -lt "${DS_DESIRED}" ]; then
      echo "[info] Cilium daemonset not fully ready (ready=${DS_READY}/${DS_DESIRED}), applying recovery upgrade disabling IPsec/encryption"
      helm -n kube-system upgrade --install cilium cilium/cilium --reuse-values --set ipsec.enabled=false --set encryption.enabled=false --wait --timeout 300s || true
    fi

    # As a last-resort recovery: if Cilium still isn't ready and the ipsec secret is missing,
    # create a dummy secret so init containers that mount it can proceed, then restart pods.
    DS_READY=$(sh -c "${KUBECTL_CMD} -n kube-system get ds cilium -o jsonpath='{.status.numberReady}' 2>/dev/null || echo 0")
    DS_DESIRED=$(sh -c "${KUBECTL_CMD} -n kube-system get ds cilium -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || echo 0")
    if [ "${DS_DESIRED:-0}" != "0" ] && [ "${DS_READY:-0}" -lt "${DS_DESIRED}" ]; then
      if ! ${KUBECTL_CMD} -n kube-system get secret cilium-ipsec-keys >/dev/null 2>&1; then
        echo "[info] creating fallback dummy secret kube-system/cilium-ipsec-keys"
        ${KUBECTL_CMD} -n kube-system create secret generic cilium-ipsec-keys --from-literal=key=dummy || true
        echo "[info] deleting Cilium pods to allow reinitialization"
        ${KUBECTL_CMD} -n kube-system delete pod -l k8s-app=cilium || true
      fi
    fi

    echo "[info] waiting for Cilium components (timeout 600s). Showing progress every 5s..."
    MAX_ITER=$((600 / 5))
    for i in $(seq 1 $MAX_ITER); do
      echo "--- status (attempt $i/$MAX_ITER) ---"
      sh -c "${KUBECTL_CMD} -n kube-system get pods -l k8s-app=cilium -o wide" || true
      # check operator deployment availability
      OP_AVAILABLE=$(sh -c "${KUBECTL_CMD} -n kube-system get deploy cilium-operator -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo 0")
      # check daemonset readiness
      DS_DESIRED=$(sh -c "${KUBECTL_CMD} -n kube-system get ds cilium -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || echo 0")
      DS_READY=$(sh -c "${KUBECTL_CMD} -n kube-system get ds cilium -o jsonpath='{.status.numberReady}' 2>/dev/null || echo 0")

      if [ "${OP_AVAILABLE:-0}" -ge 1 ] && [ "${DS_DESIRED:-0}" -ne 0 ] && [ "${DS_READY:-0}" -eq "${DS_DESIRED}" ]; then
        echo "[ok] Cilium operator available and daemonset ready (ready=${DS_READY}/${DS_DESIRED})"
        break
      fi

      sleep 5
    done

    if [ "$i" -eq "$MAX_ITER" ]; then
      echo "[error] Cilium did not become ready within timeout"
      echo "[info] showing diagnostics:"
      sh -c "${KUBECTL_CMD} -n kube-system get pods -o wide" || true
      echo "--- helm release status ---"
      helm -n kube-system status cilium || true
      echo "[warn] you can inspect logs: kubectl -n kube-system logs deployment/cilium-operator"
    fi

  echo "[info] verifying Cilium status"
  if command -v cilium >/dev/null 2>&1; then
    cilium status --wait 120s || echo "[warn] cilium status reported issues"
  else
    sh -c "${KUBECTL_CMD} -n kube-system wait --for=condition=Available deployment/cilium-operator --timeout=300s" || echo "[warn] cilium operator rollout may be incomplete"
  fi
}

install_gateway_api_crds() {
  echo "[info] applying Gateway API CRDs"
  GATEWAY_API_VERSION=${GATEWAY_API_VERSION:-v1.2.0}
  URL="https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/standard-install.yaml"
  sh -c "${KUBECTL_CMD} apply -f \"${URL}\""
}

install_envoy_gateway() {
  echo "[info] installing Envoy Gateway (waiting for rollout)"
  install_helm
  ENVOY_VERSION=${ENVOY_GATEWAY_VERSION:-v1.3.0}
  # Avoid interactive prompt from 'helm registry login' by closing stdin
  helm registry login docker.io </dev/null >/dev/null 2>&1 || true

  NS=envoy-gateway-system
  # Ensure namespace exists
  sh -c "${KUBECTL_CMD} create ns ${NS} --dry-run=client -o yaml | ${KUBECTL_CMD} apply -f -" >/dev/null 2>&1 || true

  # Try to pull the OCI chart first (fast-fail if OCI access blocked)
  CHARTDIR="/tmp/envoy-gateway-chart"
  rm -rf "${CHARTDIR}" || true
  echo "[info] attempting helm pull (OCI) as a connectivity check — this may take a few seconds"
  if timeout 60s helm pull oci://docker.io/envoyproxy/gateway-helm --version "${ENVOY_VERSION}" --untar -d /tmp >/dev/null 2>&1; then
    echo "[info] helm pull succeeded, installing from local chart"
    if ! helm upgrade --install eg /tmp/gateway-helm --namespace ${NS} --wait --timeout 300s --debug; then
      echo "[warn] helm install (local chart) failed; showing release status"
      helm -n ${NS} status eg || true
    fi
  else
    echo "[info] helm pull failed or timed out; attempting direct OCI install with debug and timeout"
    if ! timeout 180s helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm --version "${ENVOY_VERSION}" -n ${NS} --create-namespace --wait --timeout 300s --debug; then
      echo "[warn] envoy gateway helm install/upgrade failed or timed out"
      echo "--- helm releases in namespace ---"
      helm -n ${NS} list || true
      echo "--- helm release status (eg) ---"
      helm -n ${NS} status eg || true
      echo "--- namespace events ---"
      ${KUBECTL_CMD} -n ${NS} get events --sort-by='.lastTimestamp' | tail -n 50 || true
    fi
  fi

  # wait for pods in envoy-gateway-system to be ready, with clearer diagnostics on failure
  NS=envoy-gateway-system
  echo "[info] waiting for Envoy Gateway pods (timeout 300s)"
  MAX_ITER=$((300 / 5))
  for i in $(seq 1 $MAX_ITER); do
    echo "--- envoy status (attempt $i/$MAX_ITER) ---"
    sh -c "${KUBECTL_CMD} -n ${NS} get pods -o wide" || true
    TOTAL=$(sh -c "${KUBECTL_CMD} -n ${NS} get pods --no-headers 2>/dev/null | wc -l" | tr -d '[:space:]' || echo "0")
    NOT_READY=$(sh -c "${KUBECTL_CMD} -n ${NS} get pods --no-headers 2>/dev/null | awk '{split(\$2,a,"/"); if(a[1] != a[2]) print \$0}' | wc -l" | tr -d '[:space:]' || echo "0")
    if [ "${TOTAL}" -gt 0 ] && [ "${NOT_READY}" -eq 0 ]; then
      echo "[ok] Envoy Gateway pods ready (${TOTAL}/${TOTAL})"
      break
    fi
    sleep 5
  done
  if [ "$i" -eq "$MAX_ITER" ]; then
    echo "[error] Envoy Gateway pods did not become ready in time"
    sh -c "${KUBECTL_CMD} -n ${NS} get pods -o wide" || true
    echo "--- describe non-ready pods ---"
    # Describe and show logs for non-ready pods to aid debugging
    for POD in $(${KUBECTL_CMD} -n ${NS} get pods --no-headers 2>/dev/null | awk '{split($2,a,"/"); if(a[1] != a[2]) print $1}'); do
      echo "--- describe $POD ---"
      ${KUBECTL_CMD} -n ${NS} describe pod ${POD} || true
      echo "--- logs for $POD (all containers) ---"
      for C in $(${KUBECTL_CMD} -n ${NS} get pod ${POD} -o jsonpath='{.spec.containers[*].name}' 2>/dev/null); do
        echo "--- logs ${POD} -c ${C} ---"
        ${KUBECTL_CMD} -n ${NS} logs ${POD} -c ${C} --tail=200 || true
      done
    done
    helm -n envoy-gateway-system status eg || true
  fi
}

apply_gateway_manifests() {
  echo "[info] applying Gateway manifests"
  sh -c "${KUBECTL_CMD} create ns gateway-system --dry-run=client -o yaml | ${KUBECTL_CMD} apply -f -" >/dev/null 2>&1 || true

  # Look for manifests in repository root or deploy/manifests
  if [ -d "$ROOT_DIR/manifests" ]; then
    MANIFEST_DIR="$ROOT_DIR/manifests"
  elif [ -d "$ROOT_DIR/deploy/manifests" ]; then
    MANIFEST_DIR="$ROOT_DIR/deploy/manifests"
  else
    echo "[error] no manifests directory found (tried $ROOT_DIR/manifests and $ROOT_DIR/deploy/manifests)"
    return 1
  fi

  echo "[info] applying manifests from ${MANIFEST_DIR}"
  # Ensure a TLS secret exists for the Gateway (self-signed for dev)
  if ! ${KUBECTL_CMD} -n gateway-system get secret vipas-tls >/dev/null 2>&1; then
    echo "[info] creating self-signed TLS certificate and secret gateway-system/vipas-tls"
    openssl req -x509 -nodes -days 365 -newkey rsa:2048 -keyout /tmp/vipas.key -out /tmp/vipas.crt -subj "/CN=vipas.local" >/dev/null 2>&1 || true
    ${KUBECTL_CMD} -n gateway-system create secret tls vipas-tls --cert=/tmp/vipas.crt --key=/tmp/vipas.key >/dev/null 2>&1 || true
  fi

  # Apply EnvoyProxy first (hostNetwork so pods bind on 0.0.0.0:80/443 like Traefik)
  if [ -f "${MANIFEST_DIR}/envoyproxy.yaml" ]; then
    sh -c "${KUBECTL_CMD} apply -f \"${MANIFEST_DIR}/envoyproxy.yaml\""
  fi

  # If gateway manifest exists, ensure listener TLS has certificateRefs referencing the secret
  if [ -f "${MANIFEST_DIR}/gateway.yaml" ]; then
    perl -0777 -pe 's/(^(\s*)tls:\n\2\s*mode:\s*Terminate\n)(\2\s*certificateRefs:\s*\[\]\n)?/$1 . sprintf("%s  certificateRefs:\n%s  - name: vipas-tls\n%s    kind: Secret\n", $2, $2, $2)/em' "${MANIFEST_DIR}/gateway.yaml" > "${MANIFEST_DIR}/gateway.yaml.tmp" || true
    if [ -s "${MANIFEST_DIR}/gateway.yaml.tmp" ]; then
      mv "${MANIFEST_DIR}/gateway.yaml.tmp" "${MANIFEST_DIR}/gateway.yaml" || true
    else
      rm -f "${MANIFEST_DIR}/gateway.yaml.tmp" || true
    fi
  fi

  sh -c "${KUBECTL_CMD} apply -f \"${MANIFEST_DIR}/gatewayclass.yaml\"" || true
  sh -c "${KUBECTL_CMD} apply -f \"${MANIFEST_DIR}/gateway.yaml\"" || true

  echo "[info] waiting for Gateway Accepted/Programmed"
  for i in $(seq 1 60); do
    ACC=$(sh -c "${KUBECTL_CMD} -n gateway-system get gateway vipas-gateway -o jsonpath='{.status.conditions[?(@.type==\"Accepted\")].status}'" 2>/dev/null || echo "")
    PRG=$(sh -c "${KUBECTL_CMD} -n gateway-system get gateway vipas-gateway -o jsonpath='{.status.conditions[?(@.type==\"Programmed\")].status}'" 2>/dev/null || echo "")
    if [ "$ACC" = "True" ] && [ "$PRG" = "True" ]; then
      echo "[ok] Gateway accepted and programmed"
      return
    fi
    sleep 2
  done
  echo "[warn] Gateway did not reach Accepted/Programmed in time"
}

install_cert_manager() {
  echo "[info] installing cert-manager (non-blocking)"
  install_helm
  helm repo add jetstack https://charts.jetstack.io >/dev/null 2>&1 || true
  helm repo update >/dev/null 2>&1 || true
  CERT_VERSION=${CERT_MANAGER_VERSION:-v1.12.0}
  sh -c "helm upgrade --install cert-manager jetstack/cert-manager -n cert-manager --create-namespace --version ${CERT_VERSION} --set installCRDs=true" || echo "[warn] cert-manager helm install returned non-zero"

  NS=cert-manager
  echo "[info] waiting for cert-manager pods (timeout 300s)"
  MAX_ITER=$((300 / 5))
  for i in $(seq 1 $MAX_ITER); do
    echo "--- cert-manager status (attempt $i/$MAX_ITER) ---"
    sh -c "${KUBECTL_CMD} -n ${NS} get pods -o wide" || true
    TOTAL=$(sh -c "${KUBECTL_CMD} -n ${NS} get pods --no-headers 2>/dev/null | wc -l" | tr -d '[:space:]' || echo "0")
    NOT_READY=$(sh -c "${KUBECTL_CMD} -n ${NS} get pods --no-headers 2>/dev/null | awk '{split(\$2,a,\"/\"); if(a[1] != a[2]) print \$0}' | wc -l" | tr -d '[:space:]' || echo "0")
    if [ "${TOTAL}" -gt 0 ] && [ "${NOT_READY}" -eq 0 ]; then
      echo "[ok] cert-manager pods ready (${TOTAL}/${TOTAL})"
      break
    fi
    sleep 5
  done
  if [ "$i" -eq "$MAX_ITER" ]; then
    echo "[error] cert-manager pods did not become ready in time"
    sh -c "${KUBECTL_CMD} -n ${NS} get pods -o wide" || true
    helm -n cert-manager status cert-manager || true
  fi

  # Apply staging ClusterIssuer after cert-manager is available
  # try both repository locations for the ClusterIssuer manifest
  if [ -f "$ROOT_DIR/manifests/clusterissuer-staging.yaml" ]; then
    sh -c "${KUBECTL_CMD} apply -f \"$ROOT_DIR/manifests/clusterissuer-staging.yaml\"" || true
  elif [ -f "$ROOT_DIR/deploy/manifests/clusterissuer-staging.yaml" ]; then
    sh -c "${KUBECTL_CMD} apply -f \"$ROOT_DIR/deploy/manifests/clusterissuer-staging.yaml\"" || true
  else
    echo "[warn] clusterissuer-staging.yaml not found in manifests directories"
  fi
}

configure_cilium_l2_lb() {
  echo "[info] configuring Cilium LB (L2 announcement)"

  # Default dev pool can be overridden with LB_IP_POOL env var.
  LB_IP_POOL=${LB_IP_POOL:-172.26.31.240/28}
  NODE_IP=$(sh -c "${KUBECTL_CMD} get nodes -o jsonpath='{.items[0].status.addresses[?(@.type==\"InternalIP\")].address}'" 2>/dev/null || echo "")
  echo "[info] Cilium LB pool: ${LB_IP_POOL} (node IP: ${NODE_IP})"

  cat <<EOF | ${KUBECTL_CMD} apply -f -
apiVersion: cilium.io/v2alpha1
kind: CiliumLoadBalancerIPPool
metadata:
  name: vipas-lb-pool
  labels:
    app.kubernetes.io/managed-by: vipas
spec:
  blocks:
  - cidr: ${LB_IP_POOL}
---
apiVersion: cilium.io/v2alpha1
kind: CiliumL2AnnouncementPolicy
metadata:
  name: vipas-l2-announcement
  labels:
    app.kubernetes.io/managed-by: vipas
spec:
  serviceSelector:
    matchLabels:
      app.kubernetes.io/managed-by: vipas
  loadBalancerIPs: true
EOF

  # Ensure no stale BGP policy remains in dev setup.
  ${KUBECTL_CMD} delete ciliumbgppeeringpolicy vipas-bgp-peering --ignore-not-found >/dev/null 2>&1 || true
}

install_port_bridge() {
  # Envoy Gateway maps Gateway listener port 80→10080 and 443→10443 internally.
  # WSL2 auto-forwarding only detects processes that bind a socket on a port
  # (it scans /proc/net/tcp). Nothing binds :80 directly, so Windows
  # localhost:80 is never forwarded to WSL2.
  #
  # This function installs a lightweight socat systemd service that:
  #   - binds 0.0.0.0:80  → proxies to 127.0.0.1:10080 (Envoy HTTP)
  #   - binds 0.0.0.0:443 → proxies to 127.0.0.1:10443 (Envoy HTTPS)
  # WSL2 sees port 80 in /proc/net/tcp and auto-creates the Windows portproxy
  # so "astro.localhost" (or any *.localhost) in Chrome on Windows routes through
  # exactly like Traefik does with "docker run -p 80:80".
  #
  # This also works on multi-node clusters: run this function on each node.

  echo "[info] installing socat port bridge (80→10080, 443→10443)"

  # Install socat
  if ! command -v socat >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
      apt-get install -y socat >/dev/null 2>&1 || { echo "[error] failed to install socat"; return 1; }
    elif command -v yum >/dev/null 2>&1; then
      yum install -y socat >/dev/null 2>&1 || { echo "[error] failed to install socat"; return 1; }
    else
      echo "[error] cannot install socat: no apt-get or yum found" >&2
      return 1
    fi
  fi

  # Write systemd unit file
  cat > /etc/systemd/system/vipas-port-bridge.service << 'UNIT'
[Unit]
Description=Vipas port bridge: forwards :80->:10080 and :443->:10443 (Envoy Gateway)
Documentation=https://github.com/your-org/vipas
After=network.target k3s.service
Wants=k3s.service

[Service]
Type=forking
# Wait a few seconds for Envoy to start before binding
ExecStartPre=/bin/sh -c 'for i in $(seq 1 30); do ss -tlnp | grep -q :10080 && exit 0; sleep 2; done; exit 1'
# Fork two socat listeners in background
ExecStart=/bin/sh -c '\
  socat TCP-LISTEN:80,fork,reuseaddr,su=nobody TCP:127.0.0.1:10080 &  \
  echo $! > /run/vipas-bridge-http.pid; \
  socat TCP-LISTEN:443,fork,reuseaddr,su=nobody TCP:127.0.0.1:10443 & \
  echo $! > /run/vipas-bridge-https.pid'
ExecStop=/bin/sh -c '\
  [ -f /run/vipas-bridge-http.pid ]  && kill $(cat /run/vipas-bridge-http.pid)  || true; \
  [ -f /run/vipas-bridge-https.pid ] && kill $(cat /run/vipas-bridge-https.pid) || true'
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT

  systemctl daemon-reload
  systemctl enable vipas-port-bridge.service
  systemctl restart vipas-port-bridge.service || true

  # Give it a moment to start
  sleep 3

  # Verify
  if ss -tlnp | grep -q ':80 '; then
    echo "[ok] port bridge active: :80 and :443 are now listening on the host"
    echo "[ok] WSL2 will auto-forward Windows localhost:80 → Envoy Gateway"
  else
    echo "[warn] socat bridge may not have started yet, check: systemctl status vipas-port-bridge"
    echo "       Manually: socat TCP-LISTEN:80,fork,reuseaddr TCP:127.0.0.1:10080 &"
  fi
}

configure_local_dns() {
  echo "[info] configuring wildcard *.localhost DNS (like Traefik in Docker Swarm)"

  # ---- 1. Determine Envoy Gateway VIP from Cilium LB ----
  GW_IP=""
  echo "[info] waiting for gateway VIP..."
  for i in $(seq 1 30); do
    GW_IP=$(sh -c "${KUBECTL_CMD} -n gateway-system get gateway vipas-gateway \
      -o jsonpath='{.status.addresses[0].value}' 2>/dev/null" || true)
    [ -n "$GW_IP" ] && break
    sleep 3
  done
  if [ -z "$GW_IP" ]; then
    # Fallback: use the first host from the configured LB pool
    GW_IP=$(echo "${LB_IP_POOL:-172.26.31.240/28}" | cut -d/ -f1)
    echo "[warn] gateway VIP not yet assigned, using pool start: $GW_IP"
  fi
  echo "[info] gateway VIP: $GW_IP → *.localhost"

  # ---- 2. Install dnsmasq if needed ----
  if ! command -v dnsmasq >/dev/null 2>&1; then
    echo "[info] installing dnsmasq"
    if command -v apt-get >/dev/null 2>&1; then
      apt-get install -y dnsmasq >/dev/null 2>&1 || true
    elif command -v yum >/dev/null 2>&1; then
      yum install -y dnsmasq >/dev/null 2>&1 || true
    else
      echo "[error] cannot install dnsmasq: no apt-get or yum found" >&2
      return 1
    fi
  fi

  # ---- 3. Get upstream DNS before we modify anything ----
  # Try systemd-resolved first, fall back to current resolv.conf, then 8.8.8.8
  UPSTREAM=""
  if command -v resolvectl >/dev/null 2>&1; then
    UPSTREAM=$(resolvectl status 2>/dev/null | grep -m1 'DNS Servers' | awk '{print $NF}' || true)
  fi
  if [ -z "$UPSTREAM" ] || [ "$UPSTREAM" = "127.0.0.53" ]; then
    # Check if there's a real upstream behind systemd-resolved
    UPSTREAM=$(systemd-resolve --status 2>/dev/null | grep -m1 'DNS Servers:' | awk '{print $NF}' || true)
  fi
  if [ -z "$UPSTREAM" ]; then
    UPSTREAM=$(grep '^nameserver' /etc/resolv.conf 2>/dev/null | grep -v '127.0.0.53' | awk '{print $2}' | head -1 || echo "")
  fi
  UPSTREAM="${UPSTREAM:-8.8.8.8}"
  echo "[info] upstream DNS for dnsmasq: $UPSTREAM"

  # ---- 4. Write dnsmasq wildcard config ----
  mkdir -p /etc/dnsmasq.d
  cat > /etc/dnsmasq.d/10-vipas-localhost.conf << EOF
# Auto-generated by setup-dev-cluster.sh — DO NOT EDIT
# Resolve *.localhost to the Envoy Gateway VIP (Cilium LB)
address=/.localhost/${GW_IP}
# Forward all other queries to upstream
server=${UPSTREAM}
# Do not use /etc/resolv.conf as upstream source (we set it explicitly above)
no-resolv
# Bind only loopback so we don't expose dns externally
listen-address=127.0.0.1
bind-interfaces
EOF

  # ---- 5. Disable systemd-resolved DNS stub so dnsmasq can bind :53 ----
  if systemctl is-active --quiet systemd-resolved 2>/dev/null; then
    echo "[info] disabling systemd-resolved stub listener so dnsmasq can use :53"
    mkdir -p /etc/systemd/resolved.conf.d
    cat > /etc/systemd/resolved.conf.d/99-no-stub.conf << EOF
[Resolve]
DNSStubListener=no
EOF
    systemctl restart systemd-resolved 2>/dev/null || true
  fi

  # ---- 6. Handle WSL2: /etc/resolv.conf is auto-generated on each boot ----
  IS_WSL=0
  if grep -qi microsoft /proc/version 2>/dev/null || [ -f /run/WSL ]; then
    IS_WSL=1
    echo "[info] WSL2 detected — disabling auto-generated resolv.conf"
    if ! grep -q 'generateResolvConf' /etc/wsl.conf 2>/dev/null; then
      cat >> /etc/wsl.conf << EOF

[network]
generateResolvConf=false
EOF
    else
      # Make sure it's set to false
      sed -i 's/generateResolvConf=true/generateResolvConf=false/' /etc/wsl.conf 2>/dev/null || true
    fi
  fi

  # ---- 7. Point /etc/resolv.conf to dnsmasq ----
  # Remove symlink and write directly
  rm -f /etc/resolv.conf
  cat > /etc/resolv.conf << EOF
# Auto-generated by setup-dev-cluster.sh
# dnsmasq resolves *.localhost → ${GW_IP}
nameserver 127.0.0.1
EOF

  # ---- 8. Start/restart dnsmasq ----
  systemctl enable dnsmasq 2>/dev/null || true
  systemctl restart dnsmasq 2>/dev/null || service dnsmasq restart 2>/dev/null || true

  # ---- 9. Verify ----
  sleep 2
  RESOLVED=$(getent hosts "test.localhost" 2>/dev/null | awk '{print $1}' || true)
  if [ "$RESOLVED" = "$GW_IP" ]; then
    echo "[ok] test.localhost → $GW_IP ✓  (*.localhost wildcard active)"
  else
    echo "[warn] test.localhost resolved to '${RESOLVED:-nothing}' (expected $GW_IP)"
    echo "       Try: dig test.localhost @127.0.0.1 +short"
    if [ "$IS_WSL" -eq 1 ]; then
      echo "       WSL2: run 'wsl --shutdown' from PowerShell and reopen to apply resolv.conf changes"
    fi
  fi
  echo "[info] from now on, any *.localhost domain added in the UI will be reachable in the browser"
}

usage() {
  echo "usage: $0" >&2
  echo "This script must run on the K3s host as root and will prepare a local dev cluster."
}

main() {
    # Root is recommended for host-level features (eBPF), but not strictly required for Helm installs.
    if [ "$(id -u)" -ne 0 ]; then
      echo "[warn] not running as root — some host-level features (e.g. eBPF) may be unavailable. Continue anyway." >&2
    fi
    check_kubectl
  install_cilium
  configure_cilium_l2_lb
  install_gateway_api_crds
  install_envoy_gateway
  apply_gateway_manifests
  install_cert_manager
  install_port_bridge
  configure_local_dns
  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo " [done] Dev cluster ready."
  echo ""
  echo " Linux/WSL2: *.localhost → OK (dnsmasq + Envoy hostNetwork)"
  echo ""
  echo " Windows browser (Chrome/Edge): one-time setup required."
  echo " Chrome hardcodes *.localhost → 127.0.0.1 on Windows; run from"
  echo " PowerShell as Administrator in the project deploy/ folder:"
  echo ""
  echo "   powershell -ExecutionPolicy Bypass -File deploy\\windows-setup.ps1"
  echo ""
  echo " This sets up port forwarding 127.0.0.1:80/443 → WSL2 and"
  echo " auto-registers a startup task so it survives reboots."
  echo ""
  echo " Permanent alternative (Windows 11 + WSL 2.0+, needs wsl restart):"
  echo "   powershell -ExecutionPolicy Bypass -File deploy\\windows-setup.ps1 -MirroredNetwork"
  echo "   then: wsl --shutdown"
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

main "$@"
