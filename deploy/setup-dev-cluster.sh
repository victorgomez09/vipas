#!/bin/bash
set -e

echo "--- Iniciando configuración de VIPAS PaaS ---"

# 1. Asegurar que K3s está corriendo
if ! systemctl is-active --quiet k3s; then
    echo "Iniciando K3s..."
    sudo systemctl start k3s
fi

# 3. Exportar KUBECONFIG
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
sudo chmod 644 $KUBECONFIG

# Asegurar namespaces necesarios
kubectl create ns gateway-system --dry-run=client -o yaml | kubectl apply -f -

# 4. Instalar Cilium
echo "Configurando Cilium..."
cilium install \
  --version 1.16.5 \
  --set l2announcements.enabled=true \
  --set gatewayAPI.enabled=true \
  --set envoy.enabled=true \
  --set kubeProxyReplacement=true \
  --set k8sServiceHost=127.0.0.1 \
  --set k8sServicePort=6443 \
  --set gatewayAPI.enabled=true

# 4. Esperar a que el sistema esté listo
echo "Esperando a que Cilium esté operativo..."
cilium status --wait

# 5. Aplicar la política L2 por defecto
echo "Aplicando política L2 inicial..."
cat <<EOF | kubectl apply -f -
apiVersion: cilium.io/v2alpha1
kind: L2AnnouncementPolicy
metadata:
  name: vipas-l2-announcement
spec:
  serviceSelector: # Selecciona servicios con esta etiqueta
    matchLabels:
      app.kubernetes.io/managed-by: vipas
  loadBalancerIPs: true
  interfaces:
    - eth0
EOF

cat <<EOF | kubectl apply -f -
apiVersion: "cilium.io/v2"
kind: CiliumLoadBalancerIPPool
metadata:
  name: vipas-lb-pool
spec:
  blocks:
  - cidr: "192.168.1.200/29"
EOF

echo "--- Entorno listo para desarrollar VIPAS ---"
