# Runbook: probar una política L7 + FQDN en un cluster de desarrollo

Este runbook explica, paso a paso, cómo desplegar y verificar una política de red Cilium que combine reglas L7 (HTTP paths) y egress por FQDN en un cluster de desarrollo (single-node). Incluye manifiestos de ejemplo y comandos `kubectl` y `hubble` para validar comportamiento.

Requisitos
- Cluster Kubernetes con Cilium instalado y sus CRDs (`cilium.io/v2`).
- `kubectl` configurado para el cluster.
- (Opcional) Hubble instalado y accesible para observar tráfico L7.

Resumen de la prueba
1. Crear un namespace de prueba.
2. Desplegar una aplicación web simple (Deployment + Service) que exponga `/` y `/health`.
3. Crear `ConfigMap` `vipas-networkpolicy` con `allow_fqdns` y `http_paths` para habilitar egress y L7.
4. Opción A: dejar que la plataforma (orquestador) construya y aplique el `CiliumNetworkPolicy` leyendo el `ConfigMap`.
   Opción B: aplicar manualmente un `CiliumNetworkPolicy` equivalente para probar inmediatamente.
5. Verificar que las reglas L7 y FQDN están activas (inspección de CRD, Hubble, pruebas curl desde un pod cliente).

Comandos y manifiestos

1) Crear namespace de prueba

```bash
kubectl create ns test-ns
```

2) Desplegar aplicación de ejemplo (nginx simple)

Aplica el siguiente manifiesto `my-service.yaml`:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-service
  namespace: test-ns
spec:
  replicas: 1
  selector:
    matchLabels:
      app: my-service
  template:
    metadata:
      labels:
        app: my-service
    spec:
      containers:
        - name: nginx
          image: nginx:stable
          ports:
            - containerPort: 80
          readinessProbe:
            httpGet:
              path: /health
              port: 80
              scheme: HTTP

---
apiVersion: v1
kind: Service
metadata:
  name: my-service
  namespace: test-ns
spec:
  selector:
    app: my-service
  ports:
    - port: 80
      targetPort: 80
      protocol: TCP
  type: ClusterIP
```

```bash
kubectl apply -f my-service.yaml
kubectl -n test-ns wait --for=condition=available deployment/my-service --timeout=60s
```

3) Crear `ConfigMap` `vipas-networkpolicy` con FQDNs y rutas HTTP

Ejemplo (usa comillas correctas en tu shell):

```bash
kubectl -n test-ns create configmap vipas-networkpolicy \
  --from-literal=allow_fqdns='example.com,api.example.com' \
  --from-literal=http_paths='{"my-service":[{"port":80,"paths":["/","/health"]}] }'
```

4) Opciones para aplicar la política

- Opción A — (recomendada si tu instancia de Vipas está en ejecución):
  - Invoca el flujo de la aplicación que ejecuta `EnsureNetworkPolicy()` (por ejemplo registrando el proyecto o reinvocando el servicio que gestiona políticas). El orquestador leerá `vipas-networkpolicy` y creará/actualizará el `CiliumNetworkPolicy` en `test-ns`.

- Opción B — aplicar manualmente un `CiliumNetworkPolicy` equivalente (útil para pruebas rápidas):

Aplica el siguiente manifiesto `cilium-policy.yaml`:

```yaml
apiVersion: cilium.io/v2
kind: CiliumNetworkPolicy
metadata:
  name: vipas-isolation
  namespace: test-ns
spec:
  endpointSelector: {}
  ingress:
    # permitir dentro del mismo namespace
    - fromEndpoints:
        - matchLabels:
            kubernetes.io/metadata.name: test-ns
    # permitir desde kube-system (DNS/metrics)
    - fromEndpoints:
        - matchLabels:
            kubernetes.io/metadata.name: kube-system
    # L7: permitir requests a puerto 80 con paths especificados
    - toPorts:
        - ports:
            - port: "80"
              protocol: TCP
          rules:
            http:
              - path: "/"
              - path: "/health"
  egress:
    # permitir DNS
    - toPorts:
        - ports:
            - port: "53"
              protocol: UDP
            - port: "53"
              protocol: TCP
    # permitir egress a FQDNs ejemplo (http/https)
    - toFQDNs:
        - matchName: "example.com"
        - matchName: "api.example.com"
      toPorts:
        - ports:
            - port: "80"
              protocol: TCP
            - port: "443"
              protocol: TCP
```

```bash
kubectl apply -f cilium-policy.yaml
```

5) Verificaciones

- Verificar que el `CiliumNetworkPolicy` existe:

```bash
kubectl -n test-ns get ciliumnetworkpolicies
kubectl -n test-ns describe ciliumnetworkpolicy vipas-isolation
```

- Probar desde un pod cliente si el acceso HTTP es permitido/denegado según reglas:

```bash
kubectl -n test-ns run --rm -i --tty client --image=radial/busyboxplus:curl --restart=Never -- sh
# dentro del pod
curl -v http://my-service.test-ns.svc.cluster.local/health
curl -v http://example.com/    # prova egress FQDN
curl -v http://some-blocked.example.org/  # deberia fallar si no está permitido
exit
```

- Observar tráfico L7 con Hubble (si disponible):

```bash
hubble observe --namespace test-ns --last 2m
```

6) Limpieza

```bash
kubectl -n test-ns delete ciliumnetworkpolicy vipas-isolation || true
kubectl -n test-ns delete configmap vipas-networkpolicy || true
kubectl -n test-ns delete deployment,my-service,service my-service || true
kubectl delete ns test-ns || true
```

Notas y consejos
- Si la política L7 no parece aplicarse, verifica que Cilium dataplane tenga soporte L7/Hubble habilitado (valores Helm `hubble.ui.enabled`, `hubble.relay.enabled`).
- Para ver qué reglas están realmente programadas en endpoints: usar `cilium endpoint get <id>` (o `cilium endpoint list`) y `cilium policy get` en la máquina con Cilium CLI.
- En clusters sin Cilium, el fallback con `NetworkPolicy` no soporta L7 ni `toFQDNs` — en ese caso aplica una `NetworkPolicy` equivalente para validar aislamiento L3/L4.

¿Quieres que aplique estos manifiestos en un script `scripts/test-l7-fqdn.sh` dentro del repo para correr la prueba automáticamente? 
