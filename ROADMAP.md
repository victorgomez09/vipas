# Vipas PaaS — Roadmap HA + Gateway API

> Estado actual: K3s + Traefik (networkingv1.Ingress) + Flannel CNI  
> Objetivo: Stack unificado — Cilium + Envoy Gateway + cert-manager — igual en dev y prod

---

## Principio de diseño

**Un único stack de software en todos los entornos.** La diferencia entre dev y prod es solo topología (número de nodos), no el código ni las herramientas:

| | Dev | Prod |
|---|---|---|
| **Nodos** | 1 (todo en uno) | 3 control-plane + N workers + N gateway |
| **CNI** | Cilium (eBPF) | Cilium (eBPF) |
| **Gateway** | Envoy Gateway + `HTTPRoute` | Envoy Gateway + `HTTPRoute` |
| **TLS** | cert-manager (self-signed / sslip.io sin TLS) | cert-manager + Let's Encrypt |
| **Load Balancer** | NodePort directo (1 nodo) | MetalLB BGP o Cilium BGP |
| **Storage** | `local-path` de K3s | Longhorn (replicado ×3) |
| **PostgreSQL** | StatefulSet simple | CloudNativePG (HA) |
| **Control Plane HA** | No (1 nodo) | Sí (quorum etcd 3 nodos) |

> No hay bifurcaciones de código por entorno. Lo que funciona en dev funciona en prod.  
> Dominio dev: `<IP>.sslip.io` — sin TLS (cert-manager lo detecta igual que ahora con `isDevDomain`).

---

## Fase 1 — Instalar el stack base (dev: 1 nodo)

> Reemplazar K3s+Traefik+Flannel por K3s+Cilium+EnvoyGateway. Este es el paso más crítico — una vez hecho, el código es el mismo para siempre.

### 1.1 — K3s sin Traefik ni Flannel

- [x] **1.1.1** Actualizar `install.sh` para instalar K3s con Traefik y Flannel deshabilitados:
  ```bash
  curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="server \
      --disable=traefik \
      --flannel-backend=none \
      --disable-network-policy \
      --write-kubeconfig-mode=644" sh -
  ```
- [x] **1.1.2** Eliminar la función `wait_traefik()` de `install.sh`
- [x] **1.1.3** Eliminar la función `configure_traefik_tls()` de `install.sh`

### 1.2 — Instalar Cilium

- [x] **1.2.1** Añadir a `install.sh` la instalación de Cilium vía Helm:
  ```bash
  helm install cilium cilium/cilium \
    --namespace kube-system \
    --set kubeProxyReplacement=true \
    --set k8sServiceHost=<NODE_IP> \
    --set k8sServicePort=6443 \
    --set hubble.relay.enabled=true \
    --set hubble.ui.enabled=true
  ```
- [x] **1.2.2** Añadir validación post-instalación: `cilium status --wait`
- [x] **1.2.3** Mantener la lógica de detección de WireGuard (ahora como `--set encryption.enabled=true` de Cilium si el kernel lo soporta)

### 1.3 — Instalar Gateway API CRDs

- [x] **1.3.1** Añadir a `install.sh`:
  ```bash
  kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.0/standard-install.yaml
  ```
- [x] **1.3.2** Pinear la versión de Gateway API en `deploy/versions.env`

### 1.4 — Instalar Envoy Gateway

- [x] **1.4.1** Añadir a `install.sh`:
  ```bash
  helm install eg oci://docker.io/envoyproxy/gateway-helm \
    --version v1.3.0 \
    -n envoy-gateway-system \
    --create-namespace
  ```
- [x] **1.4.2** Crear el `GatewayClass` de Envoy en `deploy/manifests/gatewayclass.yaml`:
  ```yaml
  apiVersion: gateway.networking.k8s.io/v1
  kind: GatewayClass
  metadata:
    name: envoy-gateway
  spec:
    controllerName: gateway.envoyproxy.io/gatewayclass-controller
  ```
- [x] **1.4.3** Crear el `Gateway` central `vipas-gateway` en `deploy/manifests/gateway.yaml`:
  ```yaml
  apiVersion: gateway.networking.k8s.io/v1
  kind: Gateway
  metadata:
    name: vipas-gateway
    namespace: gateway-system
  spec:
    gatewayClassName: envoy-gateway
    listeners:
      - name: http
        port: 80
        protocol: HTTP
        allowedRoutes:
          namespaces:
            from: All
      - name: https
        port: 443
        protocol: HTTPS
        tls:
          mode: Terminate
          certificateRefs: []
        allowedRoutes:
          namespaces:
            from: All
  ```
- [x] **1.4.4** Añadir validación: esperar a que el Gateway tenga `Accepted: True` y `Programmed: True`

### 1.5 — Instalar cert-manager

- [x] **1.5.1** Añadir a `install.sh`:
  ```bash
  helm install cert-manager jetstack/cert-manager \
    -n cert-manager --create-namespace \
    --set crds.enabled=true
  ```
- [x] **1.5.2** Crear `ClusterIssuer` para Let's Encrypt staging en `deploy/manifests/clusterissuer-staging.yaml`
- [x] **1.5.3** Crear `ClusterIssuer` para Let's Encrypt producción en `deploy/manifests/clusterissuer-prod.yaml`
- [x] **1.5.4** En `install.sh`, crear el issuer staging por defecto; el admin cambia a prod desde el panel cuando tenga dominio real
- [x] **1.5.5** Para dominios `sslip.io`/`nip.io` (dev sin dominio real): cert-manager no emite cert — el HTTPRoute se crea sin TLS, comportamiento idéntico al actual `isDevDomain()`

### 1.6 — Validar stack base

- [x] **1.6.1** `cilium status` — todos los componentes OK
- [x] **1.6.2** `kubectl get gateway -n gateway-system` — `ACCEPTED: True`
- [x] **1.6.3** Desplegar una app de prueba con un `HTTPRoute`, verificar acceso HTTP
- [x] **1.6.4** En cluster con dominio real: verificar que cert-manager emite certificado TLS

---

## Fase 2 — Migración del código: IngressManager → GatewayManager

> Reemplazar la implementación de `networkingv1.Ingress` + Traefik por `HTTPRoute` de Gateway API. No hay bifurcación: una sola implementación para todos los entornos.

### 2.1 — Interfaz del orquestador (`orchestrator.go`)

- [x] **2.1.1** Reemplazar `IngressManager` por `GatewayManager`:
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
- [x] **2.1.2** Eliminar `TraefikManager` de la interfaz `Orchestrator`
- [x] **2.1.3** Reemplazar `IngressManager` por `GatewayManager` en la interfaz `Orchestrator`
- [x] **2.1.4** Actualizar `noop.go` con implementaciones vacías de los nuevos métodos

### 2.2 — Nueva implementación (`httproute.go`)

- [x]  **2.2.1** Añadir dependencia en `go.mod`:
  ```bash
  go get sigs.k8s.io/gateway-api@v1.2.0
  ```
 - [x] **2.2.2** Crear `apps/api/internal/orchestrator/k3s/httproute.go`:
  - [ ] `CreateHTTPRoute()`:
    - Si `isDevDomain(domain.Host)`: crear `HTTPRoute` solo con listener HTTP (sin TLS)
    - Si dominio real: crear `HTTPRoute` HTTPS + annotation `cert-manager.io/cluster-issuer` + `HTTPRequestRedirectFilter` para forzar HTTPS
    - `parentRefs` apuntando al `Gateway` central `vipas-gateway` en namespace `gateway-system`
    - `hostnames: [domain.Host]`
    - `backendRefs` al Service de la app con el puerto correcto
  - [ ] `UpdateHTTPRoute()` — actualiza puerto y config TLS si cambia
  - [ ] `DeleteHTTPRoute()` — elimina el `HTTPRoute`
  - [ ] `SyncHTTPRoutePorts()` — equivalente al actual `SyncIngressPorts`
  - [ ] `GetHTTPRouteStatus()` — lee `.status.parents[].conditions` (busca `Accepted` y `ResolvedRefs`)
  - [ ] `GetCertExpiry()` — lee el Secret de cert-manager del namespace de la app
  - [ ] `EnsurePanelHTTPRoute()` — equivalente al actual `EnsurePanelIngress`, apunta al Service `vipas`

 - [x] **2.2.3** Crear `apps/api/internal/orchestrator/k3s/gateway.go`:
  - [ ] `EnsureGateway()` — crea/actualiza el `GatewayClass` y el `Gateway` central si no existen (idempotente)

### 2.3 — Eliminar código Traefik

- [ ] **2.3.1** Eliminar `apps/api/internal/orchestrator/k3s/ingress.go`
- [ ] **2.3.2** Eliminar `apps/api/internal/orchestrator/k3s/traefik.go`
- [ ] **2.3.3** Eliminar el middleware `EnsureRedirectHTTPSMiddleware` (ahora lo hace `HTTPRequestRedirectFilter` nativo de Gateway API)

### 2.4 — Modelo de datos

- [ ] **2.4.1** En `model/domain.go`:
  - Renombrar `IngressReady` → `RouteReady` (mantener columna en BD con `bun:"ingress_ready"` alias temporal)
  - Eliminar `CertSecret` (cert-manager gestiona los Secrets directamente)
- [ ] **2.4.2** Crear migración de BD:
  - Renombrar columna `ingress_ready` → `route_ready` en tabla `domains`
  - Eliminar columna `cert_secret`
- [ ] **2.4.3** En `model/setting.go`, añadir constante:
  ```go
  SettingCertIssuer = "cert_issuer" // "letsencrypt-staging" | "letsencrypt-prod" | "selfsigned"
  ```

### 2.5 — Capa de servicio

 - [x] **2.5.1** Actualizar `domain_service.go`:
  - `CreateDomain()` → `orch.CreateHTTPRoute()`
  - `UpdateDomain()` → `orch.UpdateHTTPRoute()`
  - `DeleteDomain()` → `orch.DeleteHTTPRoute()`
  - `GetDomainStatus()` → `orch.GetHTTPRouteStatus()`
  - Mantener la lógica `isDevDomain()` — el HTTPRoute simplemente no tendrá TLS
 - [x] **2.5.2** Actualizar `deploy_service.go`: `SyncIngressPorts()` → `SyncHTTPRoutePorts()`
 - [x] **2.5.3** Actualizar `setting_service.go`:
  - `EnsurePanelIngress()` → `EnsurePanelHTTPRoute()`
  - Eliminar llamadas a `GetTraefikConfig`, `UpdateTraefikConfig`, `RestartTraefik`
  - Añadir gestión del `SettingCertIssuer` (staging vs prod)

### 2.6 — API HTTP

- [ ] **2.6.1** Listar y revisar todos los handlers en `apps/api/internal/api/v1/` que referencien Traefik o Ingress
- [ ] **2.6.2** Eliminar endpoints de Traefik (`GET/PUT /api/v1/settings/traefik`)
 - [x] **2.6.3** Añadir `GET /api/v1/gateway/status` — estado del `Gateway` y listeners
 - [x] **2.6.4** Añadir `GET /api/v1/gateway/routes` — lista de `HTTPRoutes` activos con su estado

### 2.7 — Verificación

- [ ] **2.7.1** `go build ./...` — sin errores de compilación
- [ ] **2.7.2** Desplegar una app en dev (single node), añadir dominio `sslip.io`, verificar que el HTTPRoute se crea y la app responde HTTP
- [ ] **2.7.3** En cluster con dominio real: añadir dominio, verificar TLS automático via cert-manager

---

## Fase 3 — Migración suave: Traefik → Envoy (clusters existentes)

> Para clusters ya en producción con Traefik e Ingresses activos — zero-downtime.

- [ ] **3.1** Instalar Cilium, Gateway API CRDs y Envoy Gateway **en paralelo** al Traefik existente
- [ ] **3.2** Ejecutar script de migración que:
  - Lee todos los `networkingv1.Ingress` con label `app.kubernetes.io/managed-by: vipas`
  - Crea el `HTTPRoute` equivalente por cada uno
  - Espera a que el `HTTPRoute` esté `Accepted: True` antes de borrar el Ingress
  - Actualiza el campo `route_ready` en BD
- [ ] **3.3** Migrar los dominios de control panel (`EnsurePanelHTTPRoute`)
- [ ] **3.4** Verificar que cert-manager ha emitido certificados para todos los dominios reales
- [ ] **3.5** Desinstalar Traefik:
  ```bash
  kubectl delete helmchart traefik -n kube-system
  kubectl delete helmchartconfig traefik -n kube-system
  ```
- [ ] **3.6** Limpiar campo `cert_secret = "traefik-acme"` en BD (ya eliminado del modelo en Fase 2)
- [ ] **3.7** Eliminar el volumen con `acme.json` de Traefik

---

## Fase 4 — Load Balancer (prod: bare-metal multi-nodo)

> En dev con 1 nodo el gateway escucha en NodePort. En prod se añade un LB real.

- [ ] **4.1** Elegir implementación:
  - [ ] **4.1.a** **MetalLB en modo BGP** — más maduro, amplia compatibilidad
  - [ ] **4.1.b** **Cilium BGP** — menos piezas (ya tenemos Cilium)
- [ ] **4.2** Instalar MetalLB (si opción A):
  ```bash
  helm install metallb metallb/metallb -n metallb-system --create-namespace
  ```
- [ ] **4.3** Crear `IPAddressPool` con el rango de IPs públicas disponibles
- [ ] **4.4** Crear `BGPPeer` apuntando al router upstream (o `L2Advertisement` si no hay BGP)
- [ ] **4.5** Configurar ECMP en el router upstream para distribuir tráfico entre nodos gateway
- [ ] **4.6** Añadir al modelo `Setting` las claves:
  - `lb_type` → `nodeport | metallb | cilium-bgp`
  - `lb_ip_pool` → rango CIDR del pool de IPs
- [ ] **4.7** Actualizar `setting_service.go` para gestionar la configuración del LB
- [ ] **4.8** Añadir endpoint `GET /api/v1/infra/lb/status` — IPs asignadas, peers BGP

---

## Fase 5 — Nodos dedicados al Gateway (prod)

> En dev el gateway corre con el resto. En prod se aísla en nodos dedicados.

- [ ] **5.1** Añadir `role = "gateway"` al modelo `ServerNode`:
  - Migración de BD: valores posibles → `worker | server | control-plane | gateway`
- [ ] **5.2** Actualizar `node_service.go` → al registrar un nodo con `role=gateway`:
  - Aplicar taint `role=gateway:NoSchedule` vía SSH + kubectl
  - Aplicar label `vipas/pool=gateway`
- [ ] **5.3** Crear `EnvoyProxy` custom resource para fijar el DaemonSet de Envoy en nodos gateway:
  ```yaml
  apiVersion: gateway.envoyproxy.io/v1alpha1
  kind: EnvoyProxy
  metadata:
    name: vipas-proxy
    namespace: gateway-system
  spec:
    provider:
      type: Kubernetes
      kubernetes:
        envoyDaemonSet:
          patch:
            type: StrategicMergePatch
            value:
              spec:
                template:
                  spec:
                    nodeSelector:
                      vipas/pool: gateway
                    tolerations:
                      - key: role
                        value: gateway
                        effect: NoSchedule
                    hostNetwork: true
  ```
- [ ] **5.4** Referenciar `EnvoyProxy` desde el `GatewayClass`
- [ ] **5.5** Añadir en la UI del panel sección "Nodos Gateway" separada de "Workers"
- [ ] **5.6** Asegurar que los workers NO toleran el taint `role=gateway`

---

## Fase 6 — Control Plane HA (prod)

> En dev hay 1 nodo de control plane. En prod se añaden 2 más para quorum etcd.

- [ ] **6.1** Elegir distribución:
  - [ ] **6.1.a** **K3s HA** — embedded etcd (`--cluster-init` en el primero, `--server` en los demás)
  - [ ] **6.1.b** **RKE2** — hardening CIS, SELinux, sin HelmChart CRD propio de K3s
- [ ] **6.2** Añadir `role = "control-plane"` al modelo `ServerNode`:
  - Migración de BD: valores → `worker | server | control-plane | gateway`
- [ ] **6.3** Actualizar `node_service.go` para gestionar el join de nodos `control-plane` con el flag `--server`
- [ ] **6.4** Actualizar `install.sh` para soportar el modo HA:
  - Primer nodo: `--cluster-init`
  - Nodos adicionales control-plane: `--server https://<VIP>:6443`
- [ ] **6.5** Configurar VIP para el API Server (puerto 6443):
  - [ ] Opción A: `kube-vip` en modo control-plane (recomendado bare-metal)
  - [ ] Opción B: HAProxy + Keepalived
- [ ] **6.6** Actualizar `kubeconfig/kubeconfig.yaml` para apuntar al VIP
- [ ] **6.7** Validar que `cluster.go` → `GetNodes()` muestra los 3 nodos con rol `control-plane`
- [ ] **6.8** Añadir en la UI badge con estado del quorum etcd (nodos activos / total)

---

## Fase 7 — Cilium: red avanzada (prod)

> En dev Cilium funciona en modo básico. En prod se activan capacidades avanzadas.

- [ ] **7.1** Migrar `networkpolicy.go` → `cilium_networkpolicy.go` usando `CiliumNetworkPolicy` (CRD `cilium.io/v2`):
  - Añadir soporte L7 (restricción por path HTTP)
  - Añadir **FQDN-aware egress** (controlar salida por dominio, no solo IP)
  - Actualizar `NetworkPolicyManager` en `orchestrator.go`
- [ ] **7.2** Activar **Hubble** (observabilidad de red L3/L4/L7):
  - Exponer la UI de Hubble en el panel de Vipas
- [ ] **7.3** Configurar Cilium BGP si se eligió en Fase 4:
  - `CiliumBGPPeeringPolicy` para anunciar rangos IP a los routers
  - `CiliumLoadBalancerIPPool`

---

## Fase 8 — external-dns: DNS automático

> Crear registros DNS automáticamente al añadir un dominio. Aplica a prod con dominio real.

- [ ] **8.1** Instalar **external-dns** configurado para observar `HTTPRoute`:
  ```bash
  helm install external-dns external-dns/external-dns \
    -n external-dns --create-namespace \
    --set provider=cloudflare \
    --set source=gateway-httproute
  ```
- [ ] **8.2** Añadir al modelo `Setting`:
  - `dns_provider` → `cloudflare | route53 | digitalocean | manual`
  - `dns_api_key` → secret cifrado (no en BD en texto plano)
  - `dns_zone` → zona DNS donde crear registros
- [ ] **8.3** Actualizar `setting_service.go` para gestionar la config de external-dns
- [ ] **8.4** En `domain_service.go`: si `dns_provider != manual`, informar al usuario que el registro A se crea automáticamente

---

## Fase 9 — Storage distribuido (prod)

> En dev se usa `local-path`. En prod se necesita storage replicado para soportar múltiples nodos.

- [ ] **9.1** Elegir:
  - [ ] **9.1.a** **Longhorn** — más sencillo, UI incluida, replicación automática
  - [ ] **9.1.b** **Rook/Ceph** — mayor rendimiento para I/O intensivo
- [ ] **9.2** Instalar Longhorn:
  ```bash
  helm install longhorn longhorn/longhorn \
    -n longhorn-system --create-namespace \
    --set defaultSettings.defaultReplicaCount=3
  ```
- [ ] **9.3** Hacer `longhorn` la `StorageClass` por defecto
- [ ] **9.4** Actualizar `storage.go`: crear PVCs con `storageClassName: longhorn` en prod; `local-path` en single-node
- [ ] **9.5** Integrar snapshots de Longhorn con `system_backup_service.go` (backup a S3)
- [ ] **9.6** Añadir UI de volúmenes en el panel (estado, replicación, snapshots)

---

## Fase 10 — Base de datos HA: CloudNativePG (prod)

- [ ] **10.1** Instalar el operador **CloudNativePG**:
  ```bash
  helm install cnpg cloudnative-pg/cloudnative-pg -n cnpg-system --create-namespace
  ```
- [ ] **10.2** En `model/database.go`: añadir campo `ha_mode bool` y `replicas int`
- [ ] **10.3** Actualizar `database.go` en el orquestador:
  - Si `ha_mode = true`: recurso `Cluster` de CloudNativePG
  - Si `ha_mode = false`: comportamiento actual (StatefulSet)
- [ ] **10.4** Adaptar `GetDatabaseCredentials()` para extraer credenciales del Secret de CloudNativePG
- [ ] **10.5** Añadir soporte para connection pooling con PgBouncer (incluido en CloudNativePG)
- [ ] **10.6** Integrar backups de CloudNativePG con `system_backup_service.go` (S3)
- [ ] **10.7** Migración de BD de Vipas para los nuevos campos en `managed_databases`

---

## Fase 11 — Observabilidad

- [ ] **11.1** Instalar **kube-prometheus-stack**:
  ```bash
  helm install kps prometheus-community/kube-prometheus-stack \
    -n monitoring --create-namespace
  ```
- [ ] **11.2** Configurar scraping de métricas de Envoy Gateway (endpoint `/stats/prometheus`)
- [ ] **11.3** Configurar scraping de métricas de Cilium y Hubble
- [ ] **11.4** Dashboards de Grafana:
  - Throughput y latencia por `HTTPRoute`
  - Estado de nodos gateway
  - Uso de red por namespace (Cilium/Hubble)
  - Estado de certificados TLS (expiración)
- [ ] **11.5** Reglas de Alertmanager:
  - Gateway sin endpoints disponibles
  - Certificado TLS con < 7 días para expirar
  - Nodo gateway CPU > 80%
  - Quorum etcd roto (< 2 de 3 nodos)
- [ ] **11.6** Integrar alertas con `notification_service.go` (email, Slack, webhook)
- [ ] **11.7** Integrar métricas de Envoy en `metrics_collector.go` — requests/s y latencia por dominio
- [ ] **11.8** Exponer Grafana desde el panel de Vipas

---

## Fase 12 — Panel de administración (UI)

- [ ] **12.1** Sección "Dominios":
  - `IngressReady` → `RouteReady`
  - Mostrar condiciones del `HTTPRoute` (`Accepted`, `ResolvedRefs`)
  - Mostrar IP asignada por el LB / NodePort
- [ ] **12.2** Sección "Gateway":
  - Estado del `Gateway` y sus listeners (`:80` / `:443`)
  - Número de `HTTPRoutes` activos
  - Métricas de tráfico agregadas
- [ ] **12.3** Sección "Nodos Gateway" separada de "Workers" en la vista del cluster
- [ ] **12.4** Settings: reemplazar configuración de Traefik por configuración de Envoy Gateway:
  - Concurrencia (worker threads)
  - Rate limiting global (`BackendTrafficPolicy`)
  - Timeouts
- [ ] **12.5** Sección "Certificados TLS":
  - Listar `Certificate` de cert-manager con estado y fecha de expiración
  - Selector de issuer: staging / producción
  - Botón de renovación manual
- [ ] **12.6** Onboarding actualizado:
  - Paso: elegir issuer (`staging` mientras se prueba, `prod` cuando el dominio esté listo)
  - Paso: configurar DNS provider (o `manual` para sslip.io)
  - Paso: configurar pool de IPs del LB (o `nodeport` en single-node)

---

## Fase 13 — Seguridad

- [ ] **13.1** **RBAC** granular: revisar que los `ServiceAccount` tienen permisos mínimos
- [ ] **13.2** **Pod Security Admission** con política `restricted` por namespace de usuario
- [ ] **13.3** `CiliumNetworkPolicy` egress por defecto: denegar todo excepto DNS y gateway
- [ ] **13.4** **Secrets en Vault** (HashiCorp Vault + External Secrets Operator) — `EnsureSecret()` delega en ESO
- [ ] **13.5** **Audit Logging** del API Server → sistema de logs centralizado
- [ ] **13.6** **seccomp** + **AppArmor** profiles en pods de las apps desplegadas
- [ ] **13.7** Escaneo de imágenes con Trivy integrado en `build_service.go`
- [ ] **13.8** Rotación automática de `HostKeyFingerprint` en `ServerNode` tras reinstalación

---

## Fase 14 — Documentación y runbooks

- [ ] **14.1** `README.md`: onboarding completo (single node + multi-node)
- [ ] **14.2** `CONTRIBUTING.md`: stack de desarrollo local con el nuevo setup
- [ ] **14.3** Runbook: fallo de un nodo gateway
- [ ] **14.4** Runbook: pérdida de quorum etcd
- [ ] **14.5** Runbook: renovación manual de certificados TLS
- [ ] **14.6** Runbook: escalado horizontal de nodos worker
- [ ] **14.7** Diagrama de arquitectura final (draw.io XML en `deploy/docs/`)

---

## Orden de ejecución

```
Fase 1  Stack base (K3s + Cilium + Envoy Gateway + cert-manager)
  └─► Fase 2  Código Go: IngressManager → GatewayManager
        └─► Fase 3  Migración suave (clusters con Traefik existente)
              ├─► Fase 4  Load Balancer (prod: MetalLB/Cilium BGP)
              │     └─► Fase 5  Nodos Gateway dedicados
              ├─► Fase 6  Control Plane HA (3 nodos etcd)
              ├─► Fase 7  Cilium red avanzada
              ├─► Fase 8  external-dns
              ├─► Fase 9  Storage Longhorn
              │     └─► Fase 10  CloudNativePG
              ├─► Fase 11  Observabilidad
              └─► Fase 13  Seguridad

Fases 1+2 ──► Fase 12  UI
Fase 3   ──► Fase 14  Documentación y runbooks
```

> **Las fases 1 y 2 desbloquean todo lo demás** — son el único trabajo de código puro. El resto son infra y configuración que se puede hacer de forma independiente.

---

## Stack de software (dev y prod)

| Componente | Tecnología | Dev (1 nodo) | Prod (multi-nodo) |
|---|---|---|---|
| Distribución K8s | K3s | ✓ | K3s HA (×3 CP) o RKE2 |
| CNI | Cilium (eBPF) | básico | L7 + Hubble + BGP |
| kube-proxy | Eliminado | ✓ | ✓ |
| Gateway Controller | Envoy Gateway | ✓ | ✓ (nodos dedicados) |
| Gateway dataplane | Envoy Proxy | 1 pod | DaemonSet en nodos GW |
| TLS | cert-manager | self-signed / sin TLS (sslip.io) | Let's Encrypt prod |
| Load Balancer | — | NodePort | MetalLB BGP / Cilium BGP |
| DNS automático | external-dns | no (sslip.io manual) | ✓ |
| Network Policy | CiliumNetworkPolicy | básica | L3/L4/L7 + FQDN egress |
| Observabilidad | Prometheus + Grafana | opcional | ✓ |
| Storage | local-path / Longhorn | local-path | Longhorn ×3 |
| PostgreSQL | StatefulSet / CloudNativePG | StatefulSet | CloudNativePG HA |
