package k3s

import (
	"context"
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/victorgomez09/vipas/apps/api/internal/orchestrator"
)

// CreateVolume creates a PersistentVolumeClaim.
func (o *Orchestrator) CreateVolume(ctx context.Context, opts orchestrator.VolumeOpts) (string, error) {
	qty, err := resource.ParseQuantity(opts.Size)
	if err != nil {
		return "", fmt.Errorf("invalid size %q: %w", opts.Size, err)
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      opts.Name,
			Namespace: opts.Namespace,
			Labels:    map[string]string{"managed-by": "vipas"},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: qty,
				},
			},
		},
	}

	if opts.StorageClass != "" {
		pvc.Spec.StorageClassName = &opts.StorageClass
	}

	_, err = o.client.CoreV1().PersistentVolumeClaims(opts.Namespace).Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil && !errors.IsAlreadyExists(err) {
		return "", fmt.Errorf("create PVC: %w", err)
	}

	return opts.Name, nil
}

// DeleteVolume deletes a PersistentVolumeClaim.
func (o *Orchestrator) DeleteVolume(ctx context.Context, name, namespace string) error {
	// Check if any pod is currently mounting this PVC
	pods, err := o.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err == nil {
		for _, pod := range pods.Items {
			for _, vol := range pod.Spec.Volumes {
				if vol.PersistentVolumeClaim != nil && vol.PersistentVolumeClaim.ClaimName == name {
					return fmt.Errorf("PVC %s/%s is in use by pod %s — stop the workload first", namespace, name, pod.Name)
				}
			}
		}
	}

	if err := o.client.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		return fmt.Errorf("delete PVC %s/%s: %w", namespace, name, err)
	}
	return nil
}

// ExpandVolume expands the size of an existing PersistentVolumeClaim.
func (o *Orchestrator) ExpandVolume(ctx context.Context, name, namespace, newSize string) error {
	pvc, err := o.client.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	newQty, err := resource.ParseQuantity(newSize)
	if err != nil {
		return fmt.Errorf("invalid size %q: %w", newSize, err)
	}

	currentQty := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	if newQty.Cmp(currentQty) <= 0 {
		return fmt.Errorf("new size %s must be larger than current size %s", newSize, currentQty.String())
	}

	pvc.Spec.Resources.Requests[corev1.ResourceStorage] = newQty
	_, err = o.client.CoreV1().PersistentVolumeClaims(namespace).Update(ctx, pvc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("expand PVC %s/%s: %w", namespace, name, err)
	}
	return nil
}

// EnsureLonghornStorageClass updates the Longhorn StorageClass parameters.
func (o *Orchestrator) EnsureLonghornStorageClass(ctx context.Context, replicas int32) error {
	dyn, err := dynamic.NewForConfig(o.config)
	if err != nil {
		return fmt.Errorf("create dynamic client: %w", err)
	}

	gvr := schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}
	scName := "longhorn"

	sc, err := dyn.Resource(gvr).Get(ctx, scName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			o.logger.Warn("Longhorn StorageClass not found, skipping update", slog.String("name", scName))
			return nil // Not an error if SC doesn't exist, just can't update it
		}
		return fmt.Errorf("get Longhorn StorageClass: %w", err)
	}

	// Update numberOfReplicas parameter
	params, found, err := unstructured.NestedStringMap(sc.Object, "parameters")
	if err != nil {
		return fmt.Errorf("read StorageClass parameters: %w", err)
	}
	if !found {
		params = make(map[string]string)
	}
	params["numberOfReplicas"] = fmt.Sprintf("%d", replicas)
	unstructured.SetNestedStringMap(sc.Object, params, "parameters")

	_, err = dyn.Resource(gvr).Update(ctx, sc, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("update Longhorn StorageClass: %w", err)
	}
	o.logger.Info("Longhorn StorageClass updated", slog.String("name", scName), slog.Int64("replicas", int64(replicas)))
	return nil
}
