# Implementaciones y funcionamiento dentro de Kubernetes

A continuación se describe, en términos operativos, qué implementaciones están disponibles y cómo funcionan dentro de un cluster Kubernetes —sin detallar cambios de código—.

## CiliumNetworkPolicy (L3/L4, L7 y FQDN)

- Qué es: un CRD de Cilium (group `cilium.io/v2`) que aplica políticas de red con capacidades L3/L4 y L7 (HTTP), además de soporte nativo para reglas FQDN en egress.
- Alcance y selector: normalmente se crea a nivel de namespace con `endpointSelector: {}` para aplicar a todos los pods del namespace.
- L3/L4 (aislamiento): la política modela un comportamiento "default-deny-ish":
  - Deniega tráfico entrante desde fuera del namespace por defecto.
  - Permite ingress desde el mismo namespace y desde `kube-system` (DNS, métricas).
  - Egress por defecto permite resolución DNS (puerto 53 TCP/UDP).
- L7 HTTP:
  - Las reglas L7 se aplican sobre puertos concretos y permiten filtrar por rutas HTTP (paths), métodos o encabezados cuando el dataplane lo soporta.
  - En operación, se define qué paths están permitidos por servicio/puerto (p. ej. `/`, `/health`).
- FQDN-aware egress:
  - Es posible permitir salidas a dominios concretos usando `toFQDNs` (coincidencia por nombre o wildcards) y combinarlas con puertos (80/443).

## Parametrización de políticas (flujo operativo)

- La plataforma utiliza un `ConfigMap` por namespace (ej. `vipas-networkpolicy`) para parametrizar reglas operativas sin tocar código:
  - `allow_fqdns`: lista coma-separada de dominios permitidos para egress (ej. `api.example.com,cdn.example.net`).
  - `http_paths`: JSON con la estructura `{ "service-name": [ { "port": NUMBER, "paths": ["/","/health"] } ] }` para declarar reglas L7 por servicio.
- El orquestador lee ese `ConfigMap` y extiende la `CiliumNetworkPolicy` con `toFQDNs` en egress y con reglas L7 asociadas a `toPorts`/puertos en ingress.

## Fallback a `networking.k8s.io/v1` (NetworkPolicy)

- Si las CRDs de Cilium no están presentes en el cluster, la plataforma aplica una `NetworkPolicy` estándar de Kubernetes con comportamiento L3/L4 equivalente.
- Consecuencia: funciona en clusters sin Cilium avanzado, pero sin L7 ni egress por FQDN.

## HTTPRoute + Envoy Gateway + TLS

- Routing: las rutas se declaran mediante `HTTPRoute` y apuntan por `parentRef` al `Gateway` central (por ejemplo `vipas-gateway` en `gateway-system`).
- Contenido de `HTTPRoute`: `hostnames`, `backendRefs` (Services) y reglas de match.
- TLS: en dominios reales se usa `cert-manager` para emitir certificados. En dominios dev (p. ej. `*.sslip.io`) no se solicita certificado y la ruta se crea sin TLS.
- Forzar HTTPS: se puede aplicar `HTTPRequestRedirectFilter` (Gateway API) o reglas en `HTTPRoute` para redirigir HTTP→HTTPS.

## Observabilidad: Hubble

- Hubble (relay + UI) proporciona trazas y métricas L3/L4/L7 cuando está habilitado en Cilium.
- Permite inspeccionar comunicaciones entre pods, ver latencias y reglas L7 aplicadas.
- Integración típica: Hubble → Prometheus/Grafana para dashboards y alertas.

## Cilium BGP y LoadBalancer IP pool (producción)

- En producción multi-nodo se usan `CiliumLoadBalancerIPPool` y `CiliumBGPPeeringPolicy` para anunciar IPs públicas vía BGP hacia routers upstream.
- En desarrollo single-node se emplea Cilium L2 Announcement o `nodeport` como fallback.

## Permisos y seguridad

- El componente que aplica estas políticas necesita permisos RBAC para crear/actualizar/eliminar las CRDs de Cilium (`ciliumnetworkpolicies`, `ciliumbgppeeringpolicies`, `ciliumloadbalancerippools`) y leer `ConfigMap` por namespace.
- En producción conviene aplicar el principio de mínimo privilegio a los ServiceAccounts que gestionan CRDs.

## Verificaciones y comandos útiles

1. Comprobar presencia de CRDs de Cilium:

```bash
kubectl api-resources | grep cilium
```

2. Ver la política aplicada en un namespace:

```bash
kubectl -n <ns> get ciliumnetworkpolicies
kubectl -n <ns> describe ciliumnetworkpolicy vipas-isolation
```

3. Observar tráfico L7 con Hubble:

```bash
hubble observe --namespace <ns> --last 1m
```

4. Ver `HTTPRoute` / `Gateway` / certificados:

```bash
kubectl get httproutes -A
kubectl get gateway -n gateway-system
kubectl get certificates -A
```

5. Verificar BGP announcements (prod): comprobar `CiliumBGPPeeringPolicy` y los anuncios en el router upstream.

## Comportamiento esperado

- Idempotencia: las definiciones se aplican de forma reconciliable; actualizar la misma política no crea duplicados.
- Separación por namespace: las políticas son `namespace`-scoped y sólo afectan pods del namespace correspondiente.
- Degradación controlada: si no hay soporte Cilium avanzado, la plataforma sigue funcionando con `NetworkPolicy` de Kubernetes, perdiendo granularidad L7/FQDN.