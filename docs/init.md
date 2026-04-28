# Instalar K3s
curl -sfL https://get.k3s.io | sh -s - \
  --flannel-backend=none \
  --disable-kube-proxy \
  --disable servicelb \
  --disable-network-policy \
  --disable traefik \
  --cluster-init

export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
sudo chmod 644 $KUBECONFIG

# Instalar cilium cli
CILIUM_CLI_VERSION=$(curl -s https://raw.githubusercontent.com/cilium/cilium-cli/main/stable.txt)
CLI_ARCH=amd64
curl -L --fail --remote-name-all https://github.com/cilium/cilium-cli/releases/download/${CILIUM_CLI_VERSION}/cilium-linux-${CLI_ARCH}.tar.gz
sudo tar xzvfC cilium-linux-${CLI_ARCH}.tar.gz /usr/local/bin
rm cilium-linux-${CLI_ARCH}.tar.gz

# Instalar cilium
cilium install \
  --version 1.19.3 \
  --set l2announcements.enabled=true \
  --set gatewayAPI.enabled=true \
  --set envoy.enabled=true \
  --set kubeProxyReplacement=true \
  --set k8sServiceHost=127.0.0.1 \
  --set k8sServicePort=6443 \
  --set gatewayAPI.enabled=true

cilium status --wait

# Comprobar el estado de los pods
kubectl get po -A

# Aplicar L2 annoucement
kubectl apply -f deploy/manifests/cilium-l2-policy.yaml
kubectl apply -f deploy/manifests/cilium-ip-pool.yaml

# Verificaciones
kubectl get ciliuml2announcementpolicies.cilium.io -n kube-system
kubectl describe ciliuml2announcementpolicies.cilium.io l2-vipas-policy -n kube-system
kubectl logs -n kube-system -l k8s-app=cilium --tail=100 | grep -i "l2announcement" | grep "test-vipas-service"

# Instalar envoy gateway
kubectl kustomize "github.com/kubernetes-sigs/gateway-api/config/crd?ref=v1.2.0" | kubectl apply -f -

# Verificar
kubectl get crd | grep gateway.networking.k8s.io