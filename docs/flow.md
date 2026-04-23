# Flujo de funcionamiento de la aplicación (resumen)

Este documento describe cómo fluye una petición/operación desde la UI/cliente hasta los recursos Kubernetes gestionados por Vipas.

1) Usuario / Panel UI
- El usuario interactúa con el Panel (UI) para crear proyectos, registrar dominios, desplegar aplicaciones, o cambiar settings.
- La UI hace llamadas HTTP al API (apps/api).

2) API HTTP (apps/api)
- Handlers en `apps/api/internal/api/v1/` validan entrada y llaman a los servicios (`service/*`).
- Ejemplos: `domain_service.go`, `deploy_service.go`, `project_service.go`.

3) Capa de servicio
- Los servicios encapsulan lógica de negocio y orquestación.
- P. ej., `domain_service.CreateDomain()` delega en el `orchestrator` para crear rutas y obtener estado TLS.

4) Orquestador (implementación K3s)
- `apps/api/internal/orchestrator` define interfaces (ej. `GatewayManager`, `NetworkPolicyManager`).
- K3s implementa estas interfaces en `apps/api/internal/orchestrator/k3s`.

5) Operaciones con Kubernetes
- Creación de rutas HTTP: `httproute.go` (crea/actualiza/elimina `HTTPRoute`) usando la API Gateway (parentRef a `vipas-gateway`).
  - Si dominio es dev (`*.sslip.io`), no se configura TLS.
  - Si dominio es real, se añade referencia a `cert-manager` (issuer) y un redirect HTTPS cuando aplica.

- Network policies: `EnsureNetworkPolicy()` intenta usar `CiliumNetworkPolicy` (CRD) y cae a `networking.k8s.io/v1 NetworkPolicy` si la CRD no está disponible.
  - `cilium_networkpolicy.go` construye y aplica el CRD dinámicamente.
  - Para reglas L7 y FQDN, lee `ConfigMap` `vipas-networkpolicy` en el namespace.
    - `allow_fqdns`: dominios permitidos para egress
    - `http_paths`: JSON por servicio/puerto/paths para reglas L7 HTTP

- Despliegue de aplicaciones: el orquestador crea `Deployment`, `Service`, `ConfigMap`, `Secrets` y expone puertos que luego son referenciados por `HTTPRoute`.

6) Load balancer y dirección externa
- En dev single-node se usa Cilium L2 Announcement (no servicio externo real), en prod se configura Cilium BGP para anunciar IPs públicas.
- La IP externa del Gateway se consulta via `GatewayManager.GetGatewayIP()` y se usa para instruir DNS/manual mapping.

7) Certificados TLS
- `cert-manager` gestiona issuance; las `HTTPRoute` apuntan al issuer seleccionado (`SettingCertIssuer`).
- En dev (domains sslip.io) no se emite certificado (workflow mantiene compatibilidad con `isDevDomain`).

8) Observabilidad
- Hubble está habilitado vía Helm values de Cilium (relay + UI). Las métricas y traces L3/L4/L7 pueden integrarse en Prometheus/Grafana.

Resumen de responsabilidades
- UI → API handlers → services → orchestrator (K3s implementation) → Kubernetes resources.
- Orquestador centraliza operaciones idempotentes (create/update/delete) y mantiene la lógica de rollbacks y validaciones.

Puntos para pruebas end-to-end
- Desplegar una app con Service y ver que `HTTPRoute` se crea y es `Accepted: True`.
- Crear `vipas-networkpolicy` ConfigMap y verificar que aparece un `CiliumNetworkPolicy` con reglas L7/FQDN.
- Cambiar issuer en settings y verificar que `HTTPRoute` contiene anotación para `cert-manager`.
