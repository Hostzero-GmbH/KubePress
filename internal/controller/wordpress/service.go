package wordpress

import (
	"context"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	crmv1 "hostzero.de/m/v2/api/v1"
)

// ReconcileService creates or updates the Service for WordPress
func ReconcileService(ctx context.Context, r client.Client, scheme *runtime.Scheme, wp *crmv1.WordPressSite) (string, error) {
	logger := log.FromContext(ctx).WithValues("component", "service")

	labels := GetWordpressLabels(wp, map[string]string{
		"app.kubernetes.io/name": "wordpress-service",
	})

	// Replace service name construction with:
	serviceName := GetResourceName(wp.Name)

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceName,
			Namespace: wp.Namespace,
			Labels:    labels,
			// Add annotations map if needed
			Annotations: map[string]string{},
		},
	}

	// Create or update the Service
	_, err := ctrl.CreateOrUpdate(ctx, r, service, func() error {
		service.Spec.Selector = GetWordpressLabels(wp)
		service.Spec.Ports = []corev1.ServicePort{
			// Internal routing with http (Ingress should terminate TLS)
			// and forward to the WordPress container
			{
				Name:       "http",
				Port:       80,
				TargetPort: intstr.FromInt(80),
				Protocol:   corev1.ProtocolTCP,
			},
		}

		return controllerutil.SetControllerReference(wp, service, scheme)
	})

	if err != nil {
		logger.Error(err, "Failed to reconcile Service", "name", service.Name)
		return "", err
	}

	return serviceName, nil
}
