# Balanceador de carga (MetalLB) — Notas de implementación

Este documento explica cómo se ha integrado el balanceador de carga en Vipas, los modos disponibles y cómo operarlo.

## Resumen

Vipas soporta balanceo de carga en bare-metal para entornos de producción mediante MetalLB (modo Layer2 o BGP) y, en iteraciones futuras, opcionalmente Cilium BGP. El objetivo es mantener el mismo stack en dev y prod; en producción hay que configurar un pool de IPs externas y peers BGP.

## Qué se ha implementado

- Instalador (`install.sh`):
  - Instala el chart de Helm de MetalLB por defecto.
  - Si `METALLB_IP_POOL` está definido en el fichero de entorno del instalador, aplica un manifiesto `IPAddressPool` + `L2Advertisement` (`deploy/manifests/metallb-ip-pool.yaml`).
  - Si `METALLB_BGP_PEERS` está definido (entradas separadas por comas), renderiza y aplica manifiestos `BGPPeer` a partir de `deploy/manifests/metallb-bgp-peer.yaml`.
  - Formato de entrada para BGPPeer: `peerAddress:peerASN[:sourceAddress[:password]]` (ejemplo: `203.0.113.1:65001`).

- API / Orquestador:
  - `EnsureLoadBalancer` (orquestador k3s) crea `IPAddressPool` y `L2Advertisement` para operar MetalLB en L2.
  - `GetLoadBalancerStatus` detecta los `IPAddressPool` configurados, las IPs asignadas a Services de tipo `LoadBalancer` y los recursos `BGPPeer` de MetalLB (si existen), devolviendo un JSON `LBStatus`.
  - Existen claves en `Setting` para la configuración en tiempo de ejecución: `lb_type`, `lb_ip_pool` y `gateway_ip`.
  - Endpoint: `GET /api/v1/infra/lb/status` devuelve `type`, `ip_pools`, `assigned_ips` y `bgp_peers`.

- UI:
  - El panel de Settings muestra una tarjeta del Load Balancer con `type`, `ip_pools`, `assigned_ips` y `bgp_peers`.
  - Los administradores pueden cambiar `lb_type` y `lb_ip_pool` desde la interfaz (se guardan en Settings).

## Notas operativas

- Modo Layer2 (`L2Advertisement`): se usa en redes LAN donde el router upstream no soporta BGP. Vipas crea un `IPAddressPool` y un `L2Advertisement`; MetalLB responde a ARP/NDP por las direcciones anunciadas.

- Modo BGP: MetalLB establece peerings BGP y anuncia el pool de IPs al router upstream. Para alta disponibilidad en producción, configura el router upstream para aceptar rutas idénticas desde múltiples gateways y balancearlas mediante ECMP.

- ECMP / Configuración del router: Vipas anuncia prefijos mediante MetalLB BGP; el router externo debe permitir multipath (por ejemplo `maximum-paths`/`multipath`) y aceptar múltiples next-hops para la misma prefija. Consulta `ROADMAP.md` para ejemplos (FRR, BIRD, Cisco).

## Ejemplo de entradas en el `.env` del instalador

```
METALLB_IP_POOL=198.51.100.10-198.51.100.20
METALLB_BGP_PEERS=203.0.113.1:65001,203.0.113.2:65001
LB_TYPE=metallb
```

- Al usar BGP, asegúrate de que el router upstream esté configurado para hacer peering con las direcciones indicadas y que el enrutamiento/ECMP esté habilitado.

## Próximas mejoras

- Gestionar recursos `BGPPeer` desde la API (crear/actualizar/eliminar) en lugar de depender únicamente del instalador.
- Añadir acciones en la UI para añadir/eliminar peers BGP y validación de entrada.
- Implementar recursos BGP de Cilium como alternativa al modo MetalLB.
- Añadir monitorización del estado de sesiones BGP en la UI (requiere acceso al router o al estado del hablante BGP via FRR/BIRD/metrics).

---
