#!/bin/bash
set -e

echo "--- Iniciando configuración de VIPAS PaaS ---"

# 1. Asegurar que K3s está corriendo
if ! systemctl is-active --quiet k3s; then
    echo "Iniciando K3s..."
    sudo systemctl start k3s
fi

# 2. ESPERA ACTIVA: No continuar hasta que el puerto 6443 esté abierto
# echo "Esperando a que el API Server esté listo..."
# until curl -skf https://127.0.0.1:6443/version > /dev/null; do
#     echo "Esperando por K3s (puerto 6443)..."
#     sleep 2
# done
# echo "API Server disponible."

# 3. Exportar KUBECONFIG
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
sudo chmod 644 $KUBECONFIG

# 4. Instalar Cilium
echo "Configurando Cilium..."
cilium install --version 1.16.5 \
  --kubeconfig /etc/rancher/k3s/k3s.yaml \
  --set l2announcements.enabled=true \
  --set externalIPs.enabled=true

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
  nodeSelector:
    matchLabels:
      app.kubernetes.io/managed-by: "vipas"
  loadBalancerIPs: true
  interfaces:
    - eth0
EOF

echo "--- Entorno listo para desarrollar VIPAS ---"
