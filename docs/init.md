sudo /usr/local/bin/k3s-uninstall.sh

curl -sfL https://get.k3s.io | K3S_KUBECONFIG_MODE="644" INSTALL_K3S_EXEC="--flannel-backend=none --cluster-cidr=192.168.0.0/16 --disable-network-policy --disable=traefik" sh -
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
sudo chmod 644 $KUBECONFIG

kubectl apply -f https://raw.githubusercontent.com/projectcalico/calico/v3.32.0/manifests/calico.yaml
watch kubectl get pods --all-namespaces

kubectl apply -f https://raw.githubusercontent.com/metallb/metallb/v0.15.3/config/manifests/metallb-native.yaml

helm install eg oci://docker.io/envoyproxy/gateway-helm --version v1.7.2 -n envoy-gateway-system --create-namespace

kubectl create secret generic cloudflare-api-key --from-literal=apiKey=YOUR_API_TOKEN
