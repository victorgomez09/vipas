# Diferencias entre entornos: Dev vs Prod

Este archivo enumera las diferencias operativas y de configuración entre el entorno de desarrollo (single-node) y producción (multi-nodo) para Vipas.

1) Topología de nodos
- Dev: 1 nodo "all-in-one" (control plane + worker + gateway opcional).
- Prod: 3 control-plane (quorum etcd) + N workers + N nodos gateway dedicados.

2) CNI y networking
- Ambos entornos usan Cilium (eBPF).
- Dev: modo básico de Cilium y Cilium L2 Announcement para el LB (no BGP).
- Prod: Cilium con capacidades avanzadas (L7, Hubble, BGP) y `CiliumBGPPeeringPolicy` para anunciar IPs del `CiliumLoadBalancerIPPool`.

3) Gateway y routing
- Ambos usan Envoy Gateway y `HTTPRoute` de Gateway API.
- Dev: Gateway y Envoy corren en los mismos nodos (sin nodos gateway dedicados).
- Prod: Envoy dataplane (DaemonSet) corre en nodos gateway dedicados; `EnvoyProxy` CR puede fijar nodeSelector/tolerations.

4) TLS / certificados
- Dev: dominios del tipo `<IP>.sslip.io` → no se solicita certificado público; `HTTPRoute` se crea sin TLS.
- Prod: `cert-manager` + Let's Encrypt (staging/producción). El `SettingCertIssuer` controla issuer por defecto.

5) Load balancer
- Dev: `cilium-l2` (L2 announcement) o `nodeport` en entornos muy limitados.
- Prod: `cilium-bgp` para anunciar IPs públicas hacia routers BGP; se configura `CiliumLoadBalancerIPPool`.

6) Storage
- Dev: `local-path` (K3s default) suficiente para pruebas.
- Prod: Longhorn (replicado ×3) u otra solución distribuida (Rook/Ceph) para HA y replicación.

7) Bases de datos
- Dev: PostgreSQL como StatefulSet simple para test.
- Prod: CloudNativePG (operador) en modo HA, replicas y backups gestionados.

8) Observabilidad
- Dev: Hubble y métricas activadas por defecto, pero uso limitado.
- Prod: Hubble + Prometheus + Grafana + dashboards y alertas (cert expirations, Gateway/HTTPRoute issues, etc.).

9) Seguridad y políticas
- Dev: políticas más relajadas para facilitar desarrollo (p. ej. sin pod security admission estricto).
- Prod: PodSecurity `restricted`, RBAC mínimo, `CiliumNetworkPolicy` por defecto denegando egress excepto DNS y gateways, integración con Vault/ESO para secretos.

10) Operaciones y runbooks
- Dev: comandos rápidos para levantar cluster con `install.sh` y probar despliegues.
- Prod: procesos de onboarding con asignación de pool IP, configuración BGP con el router upstream, join de control-planes con `--cluster-init`, y runbooks para quorum etcd y restorations.

Resumen
- El objetivo es mantener el mismo software y flujo en ambos entornos; la diferencia principal es la topología y las capacidades de red/HA activadas en producción.
- Lo que se desarrolla y prueba en `dev` deberá funcionar en `prod` cuando la topología y los settings (issuer, IP pool, BGP peers) estén correctamente configurados.
