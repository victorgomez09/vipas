#!/bin/bash
set -e

# 1. Levantar PostgreSQL en Docker
POSTGRES_CONTAINER=vipas-postgres
POSTGRES_PASSWORD=vipas
POSTGRES_DB=vipas
POSTGRES_PORT=5432

if [ ! "$(docker ps -q -f name=$POSTGRES_CONTAINER)" ]; then
  echo "Levantando contenedor de PostgreSQL..."
  docker run -d --name $POSTGRES_CONTAINER \
    -e POSTGRES_PASSWORD=$POSTGRES_PASSWORD \
    -e POSTGRES_DB=$POSTGRES_DB \
    -p $POSTGRES_PORT:5432 \
    postgres:15
else
  echo "PostgreSQL ya está corriendo."
fi

# 2. Instalar k3s si no está instalado
if ! command -v k3s &> /dev/null; then
  echo "Instalando k3s..."
  curl -sfL https://get.k3s.io | K3S_KUBECONFIG_MODE="644" INSTALL_K3S_EXEC="--flannel-backend=none --cluster-cidr=192.168.0.0/16 --disable-network-policy --disable=traefik" sh -
  export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
else
  echo "k3s ya está instalado."
fi

# 3. Esperar a que k3s esté listo
sleep 10

# 4. Aplicar todos los manifests de deploy/manifests
MANIFESTS_DIR="$(dirname "$0")/../deploy/manifests"
echo "Aplicando manifests de $MANIFESTS_DIR..."
for dir in "$MANIFESTS_DIR"/*/; do
  kubectl apply -f "$dir"
done

echo "Listo. PostgreSQL, k3s y los manifests están aplicados."
# 3. Esperar a que k3s esté listo
sleep 10

# 4. Pedir variables necesarias si no están definidas
read -rp "IP Pool (ej: 192.168.0.0/16): " IP_POOL
read -rp "Mail para external-dns: " EXTERNAL_DNS_MAIL
read -rp "Domain filter para external-dns: " DOMAIN_FILTER

export IP_POOL
export EXTERNAL_DNS_MAIL
export DOMAIN_FILTER

# 5. Aplicar todos los manifests de deploy/manifests usando envsubst
MANIFESTS_DIR="$(dirname "$0")/../deploy/manifests"
echo "Aplicando manifests de $MANIFESTS_DIR..."
for dir in "$MANIFESTS_DIR"/*/; do
  for file in "$dir"*.yaml; do
    [ -f "$file" ] || continue
    echo "Aplicando $file ..."
    envsubst < "$file" | kubectl apply -f -
  done
done

echo "Listo. PostgreSQL, k3s y los manifests están aplicados."

echo "Listo. PostgreSQL, k3s y los manifests están aplicados."
