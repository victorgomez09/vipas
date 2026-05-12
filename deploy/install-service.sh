#!/bin/bash

# Script de instalación de Vipas como servicio nativo (Systemd + Caddy)
# Este script asume que Go, Bun y Caddy ya están instalados en el sistema.

set -e

# --- Configuración ---
INSTALL_DIR="/opt/vipas"
WEB_DIR="/var/www/vipas"
BIN_PATH="/usr/local/bin/vipas-api"

# Variables de entorno iniciales (Ajustar según sea necesario)
DB_URL="postgres://vipas:password@localhost:5432/vipas?sslmode=disable"
JWT_SECRET=$(openssl rand -hex 32)
APP_URL="http://localhost:3000"
KUBECONFIG_PATH="/etc/rancher/k3s/k3s.yaml"

echo "🚀 Iniciando despliegue de Vipas como servicio nativo..."

# 1. Verificar dependencias
command -v go >/dev/null 2>&1 || { echo "❌ Error: Go no está instalado."; exit 1; }
command -v bun >/dev/null 2>&1 || { echo "❌ Error: Bun no está instalado."; exit 1; }
command -v caddy >/dev/null 2>&1 || { echo "❌ Error: Caddy no está instalado."; exit 1; }

# 2. Compilar Backend (Go)
echo "📦 Compilando backend..."
cd apps/api
go build -o vipas-api ./cmd/server
sudo mv vipas-api $BIN_PATH
sudo chmod +x $BIN_PATH

# Debugging: Verify the binary type and execution
echo "🔍 Verificando tipo de binario en $BIN_PATH..."
file $BIN_PATH
echo "🧪 Probando ejecución del binario..."
$BIN_PATH --version || true # Run with --version or similar to test execution, suppress error if it doesn't have it
cd ../..

# 3. Construir Frontend (React + Bun)
echo "🎨 Construyendo frontend..."
cd apps/web
bun install --frozen-lockfile
bun run build
sudo mkdir -p $WEB_DIR
sudo cp -r dist/* $WEB_DIR/
cd ../..

# 4. Configurar Caddy
echo "🌐 Configurando Caddy..."
sudo tee /etc/caddy/Caddyfile <<EOF
:3000 {
    root * $WEB_DIR
    file_server

    # Proxy para la API
    handle_path /api/* {
        reverse_proxy localhost:8080
    }

    # Soporte para SPA (React Router)
    handle {
        try_files {path} /index.html
    }
}
EOF

# 5. Crear Servicio Systemd para la API
echo "⚙️ Creando servicio systemd..."
sudo mkdir -p $INSTALL_DIR
sudo tee /etc/systemd/system/vipas-api.service <<EOF
[Unit]
Description=Vipas PaaS API Service
After=network.target k3s.service docker.service postgresql.service

[Service]
Type=simple
User=root
WorkingDirectory=$INSTALL_DIR

# Entorno
Environment=GIN_MODE=release
Environment=DATABASE_URL=$DB_URL
Environment=PORT=8080
Environment=APP_URL=$APP_URL
Environment=JWT_SECRET=$JWT_SECRET
Environment=KUBECONFIG=$KUBECONFIG_PATH

ExecStart=$BIN_PATH
Restart=always
RestartSec=5

# Limitar logs
StandardOutput=append:$INSTALL_DIR/api.log
StandardError=append:$INSTALL_DIR/api.log

[Install]
WantedBy=multi-user.target
EOF

# 6. Activar y arrancar servicios
echo "🔄 Reiniciando servicios..."
sudo systemctl daemon-reload
sudo systemctl enable --now vipas-api
sudo systemctl restart caddy

echo "----------------------------------------------------"
echo "✅ Despliegue completado con éxito."
echo "📍 Panel disponible en: $APP_URL"
echo "📝 Logs de la API: $INSTALL_DIR/api.log"
echo "🛠️ Comando para ver logs: journalctl -u vipas-api -f"
echo "----------------------------------------------------"