# Verifica la política de anuncio L2
kubectl get ciliuml2announcementpolicies.cilium.io vipas-l2-announcement -o yaml
# Deberías ver una política con `metadata.name: vipas-l2-announcement` y
# `spec.serviceSelector.matchLabels.app.kubernetes.io/managed-by: vipas`.

# Verifica el pool de IPs del Load Balancer
kubectl get ciliumloadbalancerippools.cilium.io vipas-lb-pool -o yaml
# Deberías ver un pool con `metadata.name: vipas-lb-pool` y
# `spec.blocks` conteniendo el rango CIDR que configuraste (ej. "192.168.1.200/28").


# Obtén el estado del servicio de Envoy Gateway
kubectl get gateway vipas-gateway -n gateway-system -o wide
# Busca en la salida `ADDRESS`. Esta será la IP externa asignada por Cilium.

# Lista todos los servicios de tipo LoadBalancer en el clúster
kubectl get svc -A -o wide
# Deberías ver el servicio `envoy-gateway-system-vipas-gateway` en el namespace `gateway-system`
# con la IP externa asignada por Cilium.


# Primero, obtén el nombre de un pod de Cilium
CILIUM_POD=$(kubectl get pods -n kube-system -l k8s-app=cilium -o jsonpath='{.items[0].metadata.name}')

# Verifica las entradas del Load Balancer en eBPF de Cilium
kubectl exec -it -n kube-system "$CILIUM_POD" -- cilium bpf lb list
# Deberías ver entradas para las IPs de los servicios LoadBalancer.

# Verifica las entradas ARP/NDP que Cilium está anunciando
kubectl exec -it -n kube-system "$CILIUM_POD" -- cilium bpf arp list
# La IP externa de tu servicio LoadBalancer debería aparecer aquí.

# O, directamente en la tabla de vecinos del kernel del nodo (desde el pod de Cilium)
kubectl exec -it -n kube-system "$CILIUM_POD" -- ip neigh show dev eth0
# `eth0` es la interfaz de red común, podría ser otra dependiendo de tu configuración.
# Deberías ver la IP externa del LoadBalancer asociada a la dirección MAC del nodo.
