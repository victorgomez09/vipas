# Preparar un entorno de desarrollo con k3d (y notas para Codespaces/WSL)

Este documento explica cómo crear un cluster local con `k3d` preparado para probar Vipas con Cilium + Envoy Gateway + cert-manager, y las limitaciones en entornos como GitHub Codespaces o WSL.

Requisitos mínimos
- Docker 20.10+ (host), `k3d` y `helm` instalados en la máquina local.
- Espacio y permisos para crear contenedores privilegiados si se quiere eBPF (recomendado en una VM/host, no en Codespaces).

1) Crear un cluster `k3d` apto para Cilium (ejemplo)

```bash
# Crea un cluster k3d con puertos 80/443/6443 y monta /lib/modules del host
k3d cluster create vipas \
  --servers 1 \
  --agents 0 \
  -p "80:80@server:0" \
  -p "443:443@server:0" \
  -p "6443:6443@server:0" \
  --k3s-arg "--disable=traefik@server:0" \
  --k3s-arg "--write-kubeconfig-mode=644@server:0" \
  --volume /lib/modules:/lib/modules@server:0
```

Notas:
- Montar `/lib/modules` ayuda a que Cilium acceda a módulos del kernel (eBPF). No garantiza éxito en entornos con aislamiento del contenedor (p. ej. Codespaces).
- Algunos hosts requieren que el contenedor tenga privilegios para usar eBPF; eso implica riesgo de seguridad. Para pruebas, usar una VM o WSL2 con Docker en modo privilegiado.

2) Verificar kernel / eBPF

```bash
# Debe existir el filesystem bpffs
ls -ld /sys/fs/bpf
# Comprobar que WireGuard (si se quiere encryption) está disponible en el host
modprobe --dry-run wireguard || true
```

3) Ejecutar el script de preparación del repo

En el repo Vipas está el script idempotente `deploy/setup-dev-cluster.sh`. Si su `KUBECONFIG` apunta al cluster k3d creado, ejecuta:

```bash
# opcional: export KUBECTL=kubectl
export KUBECONFIG=$(k3d kubeconfig write vipas)
sudo sh deploy/setup-dev-cluster.sh
```

El script instalará Cilium (con detección de WireGuard), aplicará Gateway API CRDs, desplegará Envoy Gateway y cert-manager, y aplicará un `ClusterIssuer` staging.

4) Limitaciones en GitHub Codespaces

- Codespaces no proporciona systemd ni acceso privilegiado al kernel por defecto. Montar `/lib/modules` o correr contenedores privilegiados suele fallar.
- Recomendaciones para usar Codespaces:
  - Conectar el Codespace a un cluster remoto (por ejemplo, un k3s en una VM/servidor) y configurar `KUBECONFIG` en el Codespace.
  - Usar el modo de desarrollo sin eBPF: el script intentará instalar Cilium con `encryption.enabled=false` si detecta que eBPF no está disponible, pero algunos features (Hubble L7, BPF dataplane) no funcionarán.

5) WSL2

- WSL2 puede ejecutar Docker/`k3d` correctamente si Docker Desktop está configurado y el kernel soporta eBPF; WSL2 con un kernel moderno suele funcionar bien.
- En WSL preferible instalar `k3d` y seguir el mismo flujo descrito arriba.

6) Debug y comprobaciones rápidas

```bash
kubectl -n kube-system get pods
# si tiene cilium CLI en local:
cilium status || true
kubectl get gateway -A
kubectl get clusterissuer -A
```

7) Alternativa ligera para desarrollo en entornos limitados

- Si no puedes habilitar eBPF en tu entorno de desarrollo, puedes usar una CNI menos exigente (por ejemplo Flannel) sólo para desarrollo local con k3d, y reservar Cilium para staging/prod. Esto evita problemas de permisos en Codespaces.

8) Seguridad y limpieza

- Después de pruebas, elimina el cluster k3d con `k3d cluster delete vipas`.
- No ejecutes contenedores privilegiados en entornos compartidos o inseguros.

Si quieres, puedo:
- generar un `README` más detallado con pasos paso-a-paso y screenshots, o
- añadir un `Makefile` o `task` para automatizar la creación del cluster k3d y la ejecución del script.
