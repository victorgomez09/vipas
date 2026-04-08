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

  # Check cluster environment: k3s, k3d, kind, codespaces
  NODE_NAMES=$(sh -c "${KUBECTL_CMD} get nodes -o jsonpath='{range .items[*]}{@.metadata.name} {end}'" 2>/dev/null || echo "")
  IS_K3D=0
  if echo "$NODE_NAMES" | grep -q "k3d"; then IS_K3D=1; fi
  if [ -n "${CODESPACES}" ] || [ -n "${GITHUB_CODESPACES}" ]; then
    echo "[warn] detected Codespaces environment — privileged kernel features (eBPF) may be unavailable"
  fi

  CILIUM_VERSION=${CILIUM_HELM_VERSION:-v1.14.0}
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

    echo "[info] installing Cilium Helm chart (non-blocking)"
    helm upgrade --install cilium cilium/cilium \
      --namespace kube-system --create-namespace \
      --version ${CILIUM_VERSION} \
      --set kubeProxyReplacement=strict \
      --set k8sServiceHost=${K8S_SERVICE_HOST} \
      --set k8sServicePort=${K8S_SERVICE_PORT} \
      --set hubble.relay.enabled=true \
      --set hubble.ui.enabled=true \
      ${EXTRA_ENC_FLAG} \
      ${EXTRA_REPLICAS_FLAG} || echo "[warn] helm install returned non-zero"

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
  echo "[info] installing Envoy Gateway (non-blocking)"
  install_helm
  ENVOY_VERSION=${ENVOY_GATEWAY_VERSION:-v1.3.0}
  helm registry login docker.io >/dev/null 2>&1 || true
  sh -c "helm upgrade --install eg oci://docker.io/envoyproxy/gateway-helm --version ${ENVOY_VERSION} -n envoy-gateway-system --create-namespace" || echo "[warn] envoy gateway helm install returned non-zero"

  # wait for pods in envoy-gateway-system to be ready
  NS=envoy-gateway-system
  echo "[info] waiting for Envoy Gateway pods (timeout 300s)"
  MAX_ITER=$((300 / 5))
  for i in $(seq 1 $MAX_ITER); do
    echo "--- envoy status (attempt $i/$MAX_ITER) ---"
    sh -c "${KUBECTL_CMD} -n ${NS} get pods -o wide" || true
    TOTAL=$(sh -c "${KUBECTL_CMD} -n ${NS} get pods --no-headers 2>/dev/null | wc -l" | tr -d '[:space:]' || echo "0")
    NOT_READY=$(sh -c "${KUBECTL_CMD} -n ${NS} get pods --no-headers 2>/dev/null | awk '{split(\$2,a,\"/\"); if(a[1] != a[2]) print \$0}' | wc -l" | tr -d '[:space:]' || echo "0")
    if [ "${TOTAL}" -gt 0 ] && [ "${NOT_READY}" -eq 0 ]; then
      echo "[ok] Envoy Gateway pods ready (${TOTAL}/${TOTAL})"
      break
    fi
    sleep 5
  done
  if [ "$i" -eq "$MAX_ITER" ]; then
    echo "[error] Envoy Gateway pods did not become ready in time"
    sh -c "${KUBECTL_CMD} -n ${NS} get pods -o wide" || true
    helm -n envoy-gateway-system status eg || true
  fi
}

apply_gateway_manifests() {
  echo "[info] applying Gateway manifests"
  sh -c "${KUBECTL_CMD} create ns gateway-system --dry-run=client -o yaml | ${KUBECTL_CMD} apply -f -" >/dev/null 2>&1 || true
  sh -c "${KUBECTL_CMD} apply -f \"$ROOT_DIR/manifests/gatewayclass.yaml\"" || true
  sh -c "${KUBECTL_CMD} apply -f \"$ROOT_DIR/manifests/gateway.yaml\"" || true

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
  sh -c "${KUBECTL_CMD} apply -f \"$ROOT_DIR/manifests/clusterissuer-staging.yaml\"" || true
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
  install_gateway_api_crds
  install_envoy_gateway
  apply_gateway_manifests
  install_cert_manager
  echo "\n[done] dev cluster prepared — try: k3s kubectl get pods -A"
}

main "$@"
