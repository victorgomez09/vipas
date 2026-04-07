# Vipas PaaS — Roadmap HA + Gateway API

> Estado actual: K3s + Traefik Ingress (networkingv1.Ingress) + Flannel CNI  
> Objetivo: Alta disponibilidad con Envoy Gateway API + Cilium CNI + nodos dedicados

---

## Fase 0 — Preparación y baseline

- [ ] **0.1** Documentar el estado actual del cluster (nodos, versión K8s, versión Traefik, número de Ingresses activos)
- [ ] **0.2** Hacer snapshot/backup de etcd antes de cualquier cambio de CNI
- [ ] **0.3** Exportar todos los `networkingv1.Ingress` actuales como backup YAML (`kubectl get ingress -A -o yaml > ingress-backup.yaml`)
- [ ] **0.4** Añadir tests de smoke en CI que validen que las rutas HTTP de las apps responden 200 antes y después de la migración
- [ ] **0.5** Pinear versiones exactas de todas las dependencias a instalar (Cilium, Envoy Gateway, cert-manager, MetalLB) en un fichero `deploy/versions.env`
- [ ] **0.6** Crear entorno de staging que replique la topología de producción para validar cada fase

---

## Fase 1 — Control Plane HA

> Objetivo: eliminar el single point of failure del plano de control

- [ ] **1.1** Decidir el número mínimo de nodos de control plane (recomendado: 3 para quorum etcd)
- [ ] **1.2** Elegir distribución de K8s:
  - [ ] **1.2.a** Migrar a **K3s HA** con embedded etcd (`--cluster-init` + 2 nodos adicionales con `--server`)
  - [ ] **1.2.b** *(Alternativa)* Migrar a **RKE2** para mayor hardening (SELinux, CIS benchmark, sin HelmChart CRD propio)
- [ ] **1.3** Aprovisionar los 3 nodos de control plane con el modelo de `ServerNode` existente
  - [ ] Añadir campo `role = "control-plane"` al modelo `ServerNode` (actualmente solo `worker | server`)
  - [ ] Actualizar `node_service.go` para gestionar el join de nodos control-plane
  - [ ] Actualizar la lógica de `install.sh` para soportar el flag `--server` de K3s HA
- [ ] **1.4** Configurar un VIP/LB para el API Server (puerto 6443):
  - [ ] Opción A: `kube-vip` en modo control-plane (recomendado para bare-metal)
  - [ ] Opción B: HAProxy + Keepalived externo
- [ ] **1.5** Actualizar `kubeconfig/kubeconfig.yaml` para apuntar al VIP del API Server
- [ ] **1.6** Validar que `cluster.go` → `GetNodes()` devuelve correctamente los 3 nodos con rol `control-plane`
- [ ] **1.7** Añadir al modelo de `ServerNode` el campo `is_control_plane bool` y migración de BD correspondiente

---

## Fase 2 — CNI: migración de Flannel a Cilium

> Cilium reemplaza Flannel (CNI) y kube-proxy mediante eBPF

- [ ] **2.1** Desinstalar `kube-proxy` del cluster (`kubectl -n kube-system delete ds kube-proxy`)
- [ ] **2.2** Instalar **Cilium** con `kubeProxyReplacement=true`:
  ```bash
  helm install cilium cilium/cilium \
    --namespace kube-system \
    --set kubeProxyReplacement=true \
    --set k8sServiceHost=<VIP_API_SERVER> \
    --set k8sServicePort=6443 \
    --set hubble.relay.enabled=true \
    --set hubble.ui.enabled=true
  ```
- [ ] **2.3** Validar conectividad pod-to-pod y DNS (`cilium connectivity test`)
- [ ] **2.4** Habilitar **Hubble** (observabilidad de red L3/L4/L7 en tiempo real)
  - [ ] Exponer la UI de Hubble a través del panel de Vipas (nuevo apartado en el dashboard)
- [ ] **2.5** Migrar las `networkingv1.NetworkPolicy` existentes a `CiliumNetworkPolicy`:
  - [ ] Reescribir `networkpolicy.go` → `cilium_networkpolicy.go` usando el CRD `cilium.io/v2`
  - [ ] Añadir soporte L7 (restricción por path HTTP)
  - [ ] Añadir soporte **FQDN-aware egress** (controlar salida por nombre de dominio, no solo IP)
  - [ ] Actualizar la interfaz `NetworkPolicyManager` en `orchestrator.go` para incluir `EnsureCiliumNetworkPolicy`
  - [ ] Mantener compatibilidad temporal con `networkingv1.NetworkPolicy` durante la transición
- [ ] **2.6** Configurar **Cilium BGP** (alternativa a MetalLB, más integrada):
  - [ ] Crear `CiliumBGPPeeringPolicy` para anunciar los rangos de IP a los routers
  - [ ] Definir pool de IPs con `CiliumLoadBalancerIPPool`
- [ ] **2.7** Actualizar tests de integración del orquestador para que pasen con Cilium

---

## Fase 3 — Load Balancer: MetalLB o Cilium BGP

> Exponer IPs reales al exterior en bare-metal (sin cloud LB)

- [ ] **3.1** Elegir implementación:
  - [ ] **3.1.a** **MetalLB** en modo BGP (más maduro, amplia compatibilidad)
  - [ ] **3.1.b** **Cilium BGP** (ya incluido si se usa Cilium, menos piezas)
- [ ] **3.2** Instalar MetalLB (si se elige opción A):
  ```bash
  helm install metallb metallb/metallb -n metallb-system --create-namespace
  ```
- [ ] **3.3** Crear `IPAddressPool` con el rango de IPs públicas disponibles
- [ ] **3.4** Crear `BGPPeer` apuntando al router upstream (o `L2Advertisement` si no hay BGP disponible)
- [ ] **3.5** Configurar ECMP en el router upstream para distribuir tráfico entre nodos gateway
- [ ] **3.6** Añadir al modelo `Setting` las claves:
  - `lb_type` → `metallb | cilium-bgp`
  - `lb_ip_pool` → rango CIDR del pool de IPs
- [ ] **3.7** Actualizar `setting_service.go` para gestionar la configuración del LB
- [ ] **3.8** Añadir endpoint en la API para obtener el estado del LB (IPs asignadas, peers BGP)

---

## Fase 4 — Nodos dedicados al Gateway

> Aislar el plano de datos de entrada para que no compita con las cargas de usuario

- [ ] **4.1** Aprovisionar mínimo 2 nodos físicos/VMs dedicados al gateway (recomendado: 3 para HA)
- [ ] **4.2** Añadir el role `gateway` al modelo `ServerNode`:
  - [ ] Migración de BD: nuevo valor en el campo `role` → `worker | server | control-plane | gateway`
  - [ ] Actualizar `node_service.go` para aplicar automáticamente taint y label al añadir un nodo gateway
- [ ] **4.3** Aplicar taint y label en los nodos gateway al unirlos al cluster:
  ```bash
  kubectl taint nodes <gw-node> role=gateway:NoSchedule
  kubectl label nodes <gw-node> role=gateway vipas/pool=gateway
  ```
- [ ] **4.4** Actualizar `node_service.go` → `AddNode()` para detectar el role `gateway` y aplicar los taints automáticamente via SSH
- [ ] **4.5** Añadir en la UI del panel una sección de "Nodos Gateway" separada de los workers
- [ ] **4.6** Configurar los pods del Gateway (Envoy) con `tolerations` y `nodeSelector` hacia los nodos gateway (ver Fase 5)
- [ ] **4.7** Asegurar que los workers NO toleran el taint `role=gateway` para que las cargas de usuario no aterricen en nodos gateway

---

## Fase 5 — Gateway API: instalación de Envoy Gateway

> Reemplazar Traefik como ingress controller por Envoy Gateway (implementación de referencia de Gateway API)

- [ ] **5.1** Instalar los CRDs de **Gateway API** (v1 estable):
  ```bash
  kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml
  ```
- [ ] **5.2** Instalar **Envoy Gateway**:
  ```bash
  helm install eg oci://docker.io/envoyproxy/gateway-helm \
    --version v1.3.0 \
    -n envoy-gateway-system \
    --create-namespace
  ```
- [ ] **5.3** Crear `EnvoyProxy` custom resource con:
  - [ ] `nodeSelector: role=gateway`
  - [ ] `tolerations: role=gateway:NoSchedule`
  - [ ] `type: DaemonSet` (un pod Envoy por nodo gateway para máximo throughput)
  - [ ] `hostNetwork: true` (acceso directo a puertos :80/:443 sin NAT adicional)
- [ ] **5.4** Crear `GatewayClass` apuntando al controlador de Envoy Gateway
- [ ] **5.5** Crear el `Gateway` central (`vipas-gateway`) en namespace `gateway-system`:
  - [ ] Listener HTTP en `:80` (redirect a HTTPS)
  - [ ] Listener HTTPS en `:443` con `certificateRefs` gestionado por cert-manager
  - [ ] Configurar `allowedRoutes` para permitir `HTTPRoute` desde cualquier namespace
- [ ] **5.6** Configurar `EnvoyProxy` con parámetros de rendimiento:
  - [ ] `concurrency`: número de worker threads (= CPUs del nodo gateway)
  - [ ] `overload manager` para protección ante sobrecarga
  - [ ] Rate limiting global via `BackendTrafficPolicy`
- [ ] **5.7** Instalar **cert-manager** (reemplaza el ACME de Traefik):
  ```bash
  helm install cert-manager jetstack/cert-manager \
    -n cert-manager --create-namespace \
    --set crds.enabled=true
  ```
- [ ] **5.8** Crear `ClusterIssuer` para Let's Encrypt:
  - [ ] Staging issuer para tests
  - [ ] Production issuer (`letsencrypt-prod`)
- [ ] **5.9** Validar que cert-manager genera certificados correctamente para un dominio de prueba

---

## Fase 6 — Migración del código: IngressManager → GatewayManager

> Modificar el orquestador para usar Gateway API en lugar de `networkingv1.Ingress`

### 6.1 — Interfaz del orquestador

- [ ] **6.1.1** Renombrar `IngressManager` → `GatewayManager` en `orchestrator.go`:
  ```go
  type GatewayManager interface {
      EnsureGateway(ctx context.Context, ns, name string) error
      CreateHTTPRoute(ctx context.Context, domain *model.Domain, app *model.Application) error
      UpdateHTTPRoute(ctx context.Context, domain *model.Domain, app *model.Application) error
      DeleteHTTPRoute(ctx context.Context, domain *model.Domain) error
      SyncHTTPRoutePorts(ctx context.Context, app *model.Application) error
      GetHTTPRouteStatus(ctx context.Context, domain *model.Domain, app *model.Application) (*IngressStatus, error)
      GetCertExpiry(ctx context.Context, domain *model.Domain, app *model.Application) (*time.Time, error)
      EnsurePanelHTTPRoute(ctx context.Context, domain, httpsEmail string) error
      DeletePanelHTTPRoute(ctx context.Context) error
      HTTPRouteName(app *model.Application, host string) string
  }
  ```
- [ ] **6.1.2** Eliminar `TraefikManager` de la interfaz `Orchestrator` (o marcar como deprecated)
- [ ] **6.1.3** Añadir `GatewayManager` a la interfaz `Orchestrator`
- [ ] **6.1.4** Actualizar `noop.go` con implementaciones vacías de los nuevos métodos

### 6.2 — Implementación K3s

- [ ] **6.2.1** Crear `apps/api/internal/orchestrator/k3s/httproute.go`:
  - [ ] Importar `sigs.k8s.io/gateway-api` como dependencia en `go.mod`
  - [ ] Implementar `CreateHTTPRoute()` que cree un `gatewayv1.HTTPRoute` con:
    - `parentRefs` apuntando al Gateway central `vipas-gateway`
    - `hostnames` con el host del dominio
    - `rules` con backend al Service de la app
    - Annotation `cert-manager.io/cluster-issuer: letsencrypt-prod`
  - [ ] Implementar redirect HTTP→HTTPS mediante `HTTPRequestRedirectFilter` en el HTTPRoute (sin ingress adicional)
  - [ ] Implementar `UpdateHTTPRoute()`
  - [ ] Implementar `DeleteHTTPRoute()`
  - [ ] Implementar `SyncHTTPRoutePorts()` (equivalente al actual `SyncIngressPorts`)
  - [ ] Implementar `GetHTTPRouteStatus()` leyendo `.status.parents[].conditions`
  - [ ] Implementar `EnsurePanelHTTPRoute()` (equivalente al actual `EnsurePanelIngress`)
- [ ] **6.2.2** Crear `apps/api/internal/orchestrator/k3s/gateway.go`:
  - [ ] Implementar `EnsureGateway()` para crear/actualizar el `Gateway` central si no existe
  - [ ] Gestionar el `GatewayClass` de Envoy
- [ ] **6.2.3** Eliminar (o marcar como legacy) `ingress.go`
- [ ] **6.2.4** Eliminar `traefik.go` (ya no se gestiona Traefik)
- [ ] **6.2.5** Añadir la dependencia de Gateway API al módulo Go:
  ```bash
  go get sigs.k8s.io/gateway-api@v1.2.0
  ```

### 6.3 — Modelo de datos

- [ ] **6.3.1** Renombrar campo `IngressReady` → `RouteReady` en `model.Domain` (o añadir campo nuevo manteniendo el antiguo por compatibilidad)
- [ ] **6.3.2** Crear migración de BD para renombrar la columna `ingress_ready` → `route_ready` en tabla `domains`
- [ ] **6.3.3** Eliminar campo `CertSecret` del modelo `Domain` (ya no se usa — cert-manager gestiona los Secrets directamente)
- [ ] **6.3.4** Añadir campo `GatewayClass` a `model.Setting` para permitir cambiar el gateway controller desde el panel

### 6.4 — Capa de servicio

- [ ] **6.4.1** Actualizar `domain_service.go`:
  - [ ] `CreateDomain()`: llamar a `orch.CreateHTTPRoute()` en vez de `orch.CreateIngress()`
  - [ ] `UpdateDomain()`: llamar a `orch.UpdateHTTPRoute()`
  - [ ] `DeleteDomain()`: llamar a `orch.DeleteHTTPRoute()`
  - [ ] `GetDomainStatus()`: llamar a `orch.GetHTTPRouteStatus()`
- [ ] **6.4.2** Actualizar `deploy_service.go`:
  - [ ] Reemplazar llamadas a `SyncIngressPorts()` → `SyncHTTPRoutePorts()`
- [ ] **6.4.3** Actualizar `setting_service.go`:
  - [ ] Reemplazar `EnsurePanelIngress()` → `EnsurePanelHTTPRoute()`
  - [ ] Añadir configuración del gateway class (`envoy-gateway`)
  - [ ] Eliminar lógica de configuración de Traefik (`GetTraefikConfig`, `UpdateTraefikConfig`, `RestartTraefik`)

### 6.5 — API HTTP

- [ ] **6.5.1** Revisar los handlers en `apps/api/internal/api/v1/` que referencien Traefik o Ingress
- [ ] **6.5.2** Eliminar o deprecar el endpoint `GET /api/v1/settings/traefik` y similares
- [ ] **6.5.3** Añadir endpoint `GET /api/v1/gateway/status` para consultar el estado del Gateway y listeners
- [ ] **6.5.4** Añadir endpoint `GET /api/v1/gateway/routes` para listar todos los HTTPRoutes activos

---

## Fase 7 — external-dns: gestión automática de DNS

> Crear registros DNS automáticamente al añadir un dominio

- [ ] **7.1** Instalar **external-dns**:
  ```bash
  helm install external-dns external-dns/external-dns \
    -n external-dns --create-namespace \
    --set provider=cloudflare  # o el proveedor DNS correspondiente
  ```
- [ ] **7.2** Configurar external-dns para observar recursos `HTTPRoute` (fuente `gateway-httproute`)
- [ ] **7.3** Añadir al modelo `Setting` las claves para configurar el proveedor DNS:
  - `dns_provider` → `cloudflare | route53 | digitalocean | ...`
  - `dns_api_key` → secret cifrado
  - `dns_zone` → zona DNS en la que se crean registros
- [ ] **7.4** Actualizar `setting_service.go` para gestionar la configuración de external-dns
- [ ] **7.5** Actualizar la UI del onboarding/setup para solicitar las credenciales DNS
- [ ] **7.6** Añadir validación en `domain_service.go`: si external-dns está configurado, no requerir que el usuario cree el registro A manualmente

---

## Fase 8 — Storage distribuido

> Reemplazar el almacenamiento local (local-path) por storage replicado para soportar multi-nodo HA

- [ ] **8.1** Elegir solución de storage:
  - [ ] **8.1.a** **Longhorn** (más sencillo, UI incluida, replicación automática entre nodos)
  - [ ] **8.1.b** **Rook/Ceph** (más complejo, mayor rendimiento para workloads intensivos en I/O)
- [ ] **8.2** Instalar Longhorn (si se elige opción A):
  ```bash
  helm install longhorn longhorn/longhorn \
    -n longhorn-system --create-namespace \
    --set defaultSettings.defaultReplicaCount=3
  ```
- [ ] **8.3** Hacer `longhorn` la `StorageClass` por defecto del cluster
- [ ] **8.4** Actualizar `storage.go` en el orquestador para crear PVCs con `storageClassName: longhorn`
- [ ] **8.5** Añadir UI en el panel para visualizar el estado de los volúmenes y su replicación
- [ ] **8.6** Actualizar la lógica de backup del sistema (`system_backup_service.go`) para que funcione con PVCs Longhorn
  - [ ] Longhorn soporta snapshots y backups nativos a S3 — integrar esta API
- [ ] **8.7** Testear el failover: apagar un nodo worker con PVCs y verificar que las apps continúan sirviendo desde réplicas

---

## Fase 9 — Base de datos HA: CloudNativePG

> Reemplazar los StatefulSets simples de PostgreSQL por un operador HA

- [ ] **9.1** Instalar el operador **CloudNativePG**:
  ```bash
  helm install cnpg cloudnative-pg/cloudnative-pg -n cnpg-system --create-namespace
  ```
- [ ] **9.2** Crear un nuevo tipo de despliegue de BD en `model/database.go`:
  - [ ] Añadir campo `ha_mode bool` al modelo `ManagedDatabase`
  - [ ] Añadir campo `replicas int` (primary + read replicas)
- [ ] **9.3** Actualizar `database.go` en el orquestador:
  - [ ] Si `ha_mode = true`: crear un recurso `Cluster` de CloudNativePG en lugar de un `StatefulSet` raw
  - [ ] Si `ha_mode = false`: mantener el comportamiento actual (compatibilidad hacia atrás)
- [ ] **9.4** Implementar `GetDatabaseCredentials()` para extraer credenciales del `Secret` que genera CloudNativePG
- [ ] **9.5** Añadir soporte para **connection pooling** con PgBouncer (incluido en CloudNativePG)
- [ ] **9.6** Integrar los backups de CloudNativePG con el `system_backup_service.go` existente (backup a S3)
- [ ] **9.7** Crear migración de BD de Vipas para los nuevos campos en `managed_databases`

---

## Fase 10 — Observabilidad

> Stack de monitorización HA-aware

- [ ] **10.1** Instalar **kube-prometheus-stack** (Prometheus + Grafana + Alertmanager):
  ```bash
  helm install kps prometheus-community/kube-prometheus-stack \
    -n monitoring --create-namespace
  ```
- [ ] **10.2** Configurar scraping de métricas de Envoy Gateway (expone endpoint `/stats/prometheus` por defecto)
- [ ] **10.3** Configurar scraping de métricas de Cilium y Hubble
- [ ] **10.4** Crear dashboards de Grafana para:
  - [ ] Throughput y latencia por `HTTPRoute`
  - [ ] Estado de los nodos gateway
  - [ ] Uso de red por namespace (Cilium)
  - [ ] Estado de los certificados TLS (expiración)
- [ ] **10.5** Configurar **Alertmanager** con reglas para:
  - [ ] Gateway caído o sin endpoints disponibles
  - [ ] Certificado TLS con menos de 7 días para expirar
  - [ ] Nodo gateway con uso de CPU > 80%
  - [ ] Réplica de etcd sin quorum
- [ ] **10.6** Integrar alertas con el `notification_service.go` existente (email, Slack, webhook)
- [ ] **10.7** Exponer Grafana a través del panel de Vipas (iframe o link directo desde la UI)
- [ ] **10.8** Integrar las métricas de Envoy con el `metrics_collector.go` existente:
  - [ ] Añadir métricas de requests/s y latencia por dominio
  - [ ] Mostrar en la UI de cada aplicación las métricas de la ruta asociada

---

## Fase 11 — Mejoras de seguridad

- [ ] **11.1** Habilitar **RBAC** granular por namespace (un ServiceAccount por proyecto/namespace)
  - [ ] `serviceaccount.go` ya existe — revisar que los SA tienen permisos mínimos
- [ ] **11.2** Activar **Pod Security Admission** (PSA) con política `restricted` por defecto
- [ ] **11.3** Configurar `CiliumNetworkPolicy` de egress por defecto: denegar todo excepto DNS y el gateway
- [ ] **11.4** Añadir soporte para **Secrets en Vault** (HashiCorp Vault + External Secrets Operator)
  - [ ] `EnsureSecret()` en `orchestrator.go` podría delegar en ESO en lugar de crear Secrets de K8s directamente
- [ ] **11.5** Activar **Audit Logging** del API Server y enviar logs a un sistema centralizado
- [ ] **11.6** Configurar **seccomp** y **AppArmor** profiles en los pods de las apps desplegadas
- [ ] **11.7** Configurar escaneo de imágenes automático en el `build_service.go` (Trivy integrado)
- [ ] **11.8** Rotar automáticamente el `HostKeyFingerprint` de los `ServerNode` tras cada reinstalación

---

## Fase 12 — Migración suave: convivencia Traefik → Envoy

> Estrategia de zero-downtime durante la transición

- [ ] **12.1** Desplegar Envoy Gateway **en paralelo** a Traefik (ambos activos)
- [ ] **12.2** Migrar los dominios uno a uno actualizando el DNS para que apunten a la VIP de Envoy
- [ ] **12.3** Implementar un flag de feature en `model.Domain`: `use_gateway_api bool`
  - [ ] Si `true`: usar `CreateHTTPRoute()`
  - [ ] Si `false`: usar `CreateIngress()` (legacy, solo durante la transición)
  - [ ] Añadir endpoint de admin para cambiar el flag en masa
- [ ] **12.4** Verificar cert-manager emitiendo certificados correctamente para cada dominio migrado
- [ ] **12.5** Una vez todos los dominios migrados, desinstalar Traefik:
  ```bash
  kubectl delete helmchart traefik -n kube-system
  ```
- [ ] **12.6** Limpiar el campo `CertSecret = "traefik-acme"` de todos los dominios en BD
- [ ] **12.7** Eliminar el fichero `acme.json` del volumen de Traefik
- [ ] **12.8** Eliminar `traefik.go` del orquestador y el middleware `TraefikManager` de la interfaz

---

## Fase 13 — Panel de administración (UI)

> Adaptar el frontend a la nueva arquitectura

- [ ] **13.1** Actualizar la sección de "Dominios" en la UI:
  - [ ] Cambiar "Ingress Ready" → "Route Ready"
  - [ ] Mostrar el estado de las condiciones del `HTTPRoute` (en lugar del status del Ingress)
  - [ ] Mostrar la IP del Gateway asignada via MetalLB/BGP
- [ ] **13.2** Añadir sección "Gateway" en el panel de administración:
  - [ ] Estado del `Gateway` y sus listeners (`:80` / `:443`)
  - [ ] Número de `HTTPRoutes` activos
  - [ ] Métricas de tráfico agregadas del Gateway
- [ ] **13.3** Añadir sección "Nodos Gateway" separada de la sección "Workers" en la vista de cluster
- [ ] **13.4** Actualizar la configuración de Traefik en Settings → reemplazar por configuración de Envoy
  - [ ] Ratio de concurrencia (worker threads)
  - [ ] Rate limiting global
  - [ ] Configuración de timeouts
- [ ] **13.5** Añadir sección "Certificados TLS":
  - [ ] Listar todos los `Certificate` de cert-manager con estado y fecha de expiración
  - [ ] Botón de renovación manual forzada
- [ ] **13.6** Añadir wizard de onboarding actualizado:
  - [ ] Paso: configurar proveedor DNS (para external-dns)
  - [ ] Paso: configurar pool de IPs del LB
  - [ ] Paso: configurar email de Let's Encrypt (ya existe en `SettingHTTPSEmail`)

---

## Fase 14 — Documentación y operaciones

- [ ] **14.1** Documentar el proceso de añadir un nuevo nodo gateway al cluster
- [ ] **14.2** Documentar el proceso de escalado horizontal de los nodos worker
- [ ] **14.3** Actualizar `CONTRIBUTING.md` con el nuevo stack de desarrollo local
- [ ] **14.4** Crear runbook para recuperación ante fallo de un nodo gateway
- [ ] **14.5** Crear runbook para recuperación ante pérdida de quorum etcd
- [ ] **14.6** Crear runbook para renovación manual de certificados TLS si cert-manager falla
- [ ] **14.7** Documentar la arquitectura final con diagrama actualizado (draw.io XML en `/deploy/docs/`)

---

## Resumen de dependencias entre fases

```
Fase 0 (baseline)
  └─► Fase 1 (Control Plane HA)
        └─► Fase 2 (Cilium CNI)
              ├─► Fase 3 (MetalLB / Cilium BGP)
              │     └─► Fase 4 (Nodos Gateway dedicados)
              │           └─► Fase 5 (Envoy Gateway install)
              │                 └─► Fase 6 (Migración código)
              │                       └─► Fase 7 (external-dns)
              │                             └─► Fase 12 (Migración suave)
              └─► Fase 8 (Storage distribuido)
                    └─► Fase 9 (CloudNativePG)

Fase 5 ──────────────► Fase 10 (Observabilidad)
Fase 2 ──────────────► Fase 11 (Seguridad)
Fase 6 ──────────────► Fase 13 (UI)
Fase 12 ─────────────► Fase 14 (Documentación)
```

---

## Stack final objetivo

| Componente | Tecnología | Namespace |
|---|---|---|
| CNI | Cilium (eBPF) | `kube-system` |
| kube-proxy | Eliminado (Cilium lo reemplaza) | — |
| Load Balancer | MetalLB BGP / Cilium BGP | `metallb-system` |
| Gateway Controller | Envoy Gateway | `envoy-gateway-system` |
| Gateway (dataplane) | Envoy Proxy DaemonSet | `gateway-system` |
| TLS / Certs | cert-manager + Let's Encrypt | `cert-manager` |
| DNS automático | external-dns | `external-dns` |
| Observabilidad red | Cilium Hubble | `kube-system` |
| Métricas | Prometheus + Grafana | `monitoring` |
| Storage | Longhorn | `longhorn-system` |
| PostgreSQL HA | CloudNativePG | `cnpg-system` |
| Control Plane HA | K3s embedded etcd ×3 / RKE2 | — |
