package wordpress

import (
	"context"
	"k8s.io/apimachinery/pkg/runtime"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	crmv1 "hostzero.de/m/v2/api/v1"
)

// ReconcilePVC creates or updates the PVC for WordPress
func ReconcilePVC(ctx context.Context, r client.Client, scheme *runtime.Scheme, wp *crmv1.WordPressSite) error {
	logger := log.FromContext(ctx).WithValues("component", "ingress")

	pvcName := GetPVCName(wp.Name)

	// Check if central PVC exists
	pvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: wp.Namespace}, pvc)
	if errors.IsNotFound(err) {
		logger.Info("PVC not found, creating it", "name", pvcName)

		// Create PVC labels WITHOUT organization path
		labels := map[string]string{
			"app.kubernetes.io/name":       "wordpress",
			"app.kubernetes.io/managed-by": "kubepress-operator",
			"app.kubernetes.io/part-of":    "kubepress",
		}

		// Determine storage size, default to 10Gi for central storage
		storageSize := "10Gi"
		if wp.Spec.WordPress.StorageSize != "" {
			storageSize = wp.Spec.WordPress.StorageSize
		}

		// Always use ReadWriteMany for central storage
		accessMode := corev1.ReadWriteMany

		// Get storage class name
		storageClassName := os.Getenv("STORAGE_CLASS_NAME")
		var storageClassNamePtr *string
		if storageClassName != "" {
			storageClassNamePtr = &storageClassName
		}

		// Create the central PVC
		newPVC := &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:        pvcName,
				Namespace:   wp.Namespace,
				Labels:      labels,
				Annotations: map[string]string{},
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{
					accessMode,
				},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse(storageSize),
					},
				},
				StorageClassName: storageClassNamePtr,
			},
		}

		// Set owner reference
		if err := controllerutil.SetControllerReference(wp, newPVC, scheme); err != nil {
			logger.Error(err, "Unable to set owner reference to PVC", "object", newPVC.GetName())
			return err
		}

		// Create the PVC
		if err := r.Create(ctx, newPVC); err != nil {
			logger.Error(err, "Failed to create central PVC", "storageClass", storageClassName)
			return err
		}

		return nil
	} else if err != nil {
		return err
	} else {
		// updating existing PVC
		quantity := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		if quantity != resource.MustParse(wp.Spec.WordPress.StorageSize) {
			quantity = resource.MustParse(wp.Spec.WordPress.StorageSize)
			if err := r.Update(ctx, pvc); err != nil {
				logger.Error(err, "Failed to update PVC storage size")
				return err
			}
		}
	}

	return nil
}
