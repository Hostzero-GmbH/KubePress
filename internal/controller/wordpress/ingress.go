package wordpress

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	crmv1 "hostzero.de/m/v2/api/v1"
)

// ReconcileIngress creates or updates the Ingress for WordPress
func ReconcileIngress(ctx context.Context, r client.Client, scheme *runtime.Scheme, wp *crmv1.WordPressSite) error {
	logger := log.FromContext(ctx).WithValues("component", "ingress")

	// Determine ingress configuration values
	ingressName := GetResourceName(wp.Name)
	host, ingressNameErr := determineIngressHost(wp)
	if ingressNameErr != nil {
		logger.Error(ingressNameErr, "Failed to determine ingress host")
		return ingressNameErr
	}
	serviceName := GetResourceName(wp.Name)
	path := "/"
	pathType := networkingv1.PathTypePrefix
	ingressClassName := "nginx"
	secretName := GetTLSSecretName(wp.Name)

	// Check if ingress exists
	ingress := &networkingv1.Ingress{}
	err := r.Get(ctx, types.NamespacedName{Name: ingressName, Namespace: wp.Namespace}, ingress)

	if errors.IsNotFound(err) {
		// Create new ingress
		return createIngress(ctx, r, scheme, wp, ingressName, host, serviceName, path, pathType,
			ingressClassName, secretName, logger)
	} else if err != nil {
		logger.Error(err, "Failed to get Ingress")
		return fmt.Errorf("failed to get ingress: %w", err)
	}

	// Ingress exists - update it if needed
	return updateExistingIngress(ctx, r, wp, ingress, host, serviceName, path, pathType,
		secretName, logger)
}

// createIngress creates a new ingress resource
func createIngress(ctx context.Context, r client.Client, scheme *runtime.Scheme, wp *crmv1.WordPressSite,
	ingressName, host, serviceName, path string, pathType networkingv1.PathType,
	ingressClassName, secretName string,
	logger logr.Logger) error {

	labels := GetWordpressLabels(wp, map[string]string{
		"app.kubernetes.io/name": "wordpress-ingress",
	})

	maxUploadLimit := wp.Spec.WordPress.MaxUploadLimit
	if maxUploadLimit == "" {
		maxUploadLimit = "64M" // default value if not set
	}

	// Create ingress with basic settings
	ingress := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ingressName,
			Namespace: wp.Namespace,
			Labels:    labels,
			Annotations: map[string]string{
				"nginx.ingress.kubernetes.io/proxy-body-size":    wp.Spec.WordPress.MaxUploadLimit,
				"nginx.ingress.kubernetes.io/proxy-read-timeout": "100",
				"nginx.ingress.kubernetes.io/proxy-send-timeout": "100",
			},
		},
		Spec: networkingv1.IngressSpec{
			IngressClassName: &ingressClassName,
		},
	}

	// Configure ingress rules
	configureIngressRules(ingress, host, path, pathType, serviceName)

	// Configure TLS if enabled
	if wp.Spec.Ingress != nil && wp.Spec.Ingress.TLS {
		configureTLS(ingress, host, secretName)
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(wp, ingress, scheme); err != nil {
		logger.Error(err, "Unable to set owner reference to Ingress", "object", ingress.GetName())
		return err
	}

	// Create the ingress
	if err := r.Create(ctx, ingress); err != nil {
		logger.Error(err, "Failed to create Ingress")
		return fmt.Errorf("failed to create ingress: %w", err)
	}

	return nil
}

// updateExistingIngress updates an existing ingress if changes are needed
func updateExistingIngress(ctx context.Context, r client.Client, wp *crmv1.WordPressSite,
	ingress *networkingv1.Ingress, host, serviceName, path string, pathType networkingv1.PathType,
	secretName string,
	logger logr.Logger) error {

	needsUpdate := false

	// Update labels if needed
	labels := GetWordpressLabels(wp, map[string]string{
		"app.kubernetes.io/name": "wordpress-ingress",
	})
	if !mapsEqual(ingress.Labels, labels) {
		ingress.Labels = labels
		needsUpdate = true
	}

	// update max upload limit annotation if needed
	maxUploadLimit := wp.Spec.WordPress.MaxUploadLimit
	if maxUploadLimit == "" {
		maxUploadLimit = "64M" // default value if not set
	}

	if ingress.Annotations == nil {
		ingress.Annotations = make(map[string]string)
	}

	if ingress.Annotations["nginx.ingress.kubernetes.io/proxy-body-size"] != maxUploadLimit {
		ingress.Annotations["nginx.ingress.kubernetes.io/proxy-body-size"] = maxUploadLimit
		needsUpdate = true
	}

	// Update rules if needed
	if needsRulesUpdate(ingress, host, serviceName, path, pathType) {
		updateIngressRules(ingress, host, serviceName, path, pathType)
		needsUpdate = true
	}

	// Update TLS if needed
	tlsChanged := false
	if wp.Spec.Ingress != nil && wp.Spec.Ingress.TLS {
		tlsChanged = updateTLSConfig(ingress, host, secretName)
		if tlsChanged {
			needsUpdate = true
		}
	}

	// Update the ingress if needed
	if needsUpdate {
		if err := r.Update(ctx, ingress); err != nil {
			logger.Error(err, "Failed to update Ingress")
			return fmt.Errorf("failed to update ingress: %w", err)
		}
	}

	return nil
}

// determineIngressHost determines the host for the ingress
func determineIngressHost(wp *crmv1.WordPressSite) (string, error) {
	if wp.Spec.Ingress != nil && wp.Spec.Ingress.Host != "" {
		return wp.Spec.Ingress.Host, nil
	}
	return "", fmt.Errorf("ingress host must be specified in spec.ingress.host for WordPress site %s", wp.Name)
}

// configureIngressRules configures the rules for an ingress
func configureIngressRules(ingress *networkingv1.Ingress, host, path string,
	pathType networkingv1.PathType, serviceName string) {

	ingress.Spec.Rules = []networkingv1.IngressRule{
		{
			Host: host,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{
						{
							Path:     path,
							PathType: &pathType,
							Backend: networkingv1.IngressBackend{
								Service: &networkingv1.IngressServiceBackend{
									Name: serviceName,
									Port: networkingv1.ServiceBackendPort{
										Number: 80,
									},
								},
							},
						},
					},
				},
			},
		},
	}
}

// configureTLS configures TLS for an ingress
func configureTLS(ingress *networkingv1.Ingress, host, secretName string) {
	// add annotations for TLS if needed
	if ingress.Annotations == nil {
		ingress.Annotations = make(map[string]string)
	}

	ingress.Annotations["cert-manager.io/cluster-issuer"] = os.Getenv("TLS_CLUSTER_ISSUER")

	ingress.Spec.TLS = []networkingv1.IngressTLS{
		{
			Hosts:      []string{host},
			SecretName: secretName,
		},
	}
}

// needsRulesUpdate checks if ingress rules need to be updated
func needsRulesUpdate(ingress *networkingv1.Ingress, host, serviceName, path string,
	pathType networkingv1.PathType) bool {

	// If no rules exist or host doesn't match
	if len(ingress.Spec.Rules) == 0 || ingress.Spec.Rules[0].Host != host {
		return true
	}

	// If HTTP is nil or no paths exist
	if ingress.Spec.Rules[0].HTTP == nil || len(ingress.Spec.Rules[0].HTTP.Paths) == 0 {
		return true
	}

	// Check if path matches
	if ingress.Spec.Rules[0].HTTP.Paths[0].Path != path {
		return true
	}

	// Check if path type matches
	if ingress.Spec.Rules[0].HTTP.Paths[0].PathType == nil || *ingress.Spec.Rules[0].HTTP.Paths[0].PathType != pathType {
		return true
	}

	// Check if service name matches
	if ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service == nil ||
		ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name != serviceName {
		return true
	}

	return false
}

// updateIngressRules updates the rules for an existing ingress
func updateIngressRules(ingress *networkingv1.Ingress, host, serviceName, path string,
	pathType networkingv1.PathType) {

	// Update host if rules exist
	if len(ingress.Spec.Rules) > 0 {
		ingress.Spec.Rules[0].Host = host

		// Create HTTP if it doesn't exist
		if ingress.Spec.Rules[0].HTTP == nil {
			ingress.Spec.Rules[0].HTTP = &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{},
			}
		}

		// Add path if none exist
		if len(ingress.Spec.Rules[0].HTTP.Paths) == 0 {
			ingress.Spec.Rules[0].HTTP.Paths = []networkingv1.HTTPIngressPath{
				{
					Path:     path,
					PathType: &pathType,
					Backend: networkingv1.IngressBackend{
						Service: &networkingv1.IngressServiceBackend{
							Name: serviceName,
							Port: networkingv1.ServiceBackendPort{
								Number: 80,
							},
						},
					},
				},
			}
		} else {
			// Update existing path
			ingress.Spec.Rules[0].HTTP.Paths[0].Path = path
			ingress.Spec.Rules[0].HTTP.Paths[0].PathType = &pathType

			// Update service if it exists, otherwise create it
			if ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service != nil {
				ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name = serviceName
			} else {
				ingress.Spec.Rules[0].HTTP.Paths[0].Backend.Service = &networkingv1.IngressServiceBackend{
					Name: serviceName,
					Port: networkingv1.ServiceBackendPort{
						Number: 80,
					},
				}
			}
		}
	} else {
		// Create rules from scratch
		configureIngressRules(ingress, host, path, pathType, serviceName)
	}
}

// updateTLSConfig updates TLS configuration for an ingress if needed
func updateTLSConfig(ingress *networkingv1.Ingress, host, secretName string) bool {
	changed := false

	// If TLS is not configured, add it
	if len(ingress.Spec.TLS) == 0 {
		configureTLS(ingress, host, secretName)
		changed = true
	} else {
		// Check if TLS settings need to be updated
		if ingress.Spec.TLS[0].SecretName != secretName {
			ingress.Spec.TLS[0].SecretName = secretName
			changed = true
		}

		if len(ingress.Spec.TLS[0].Hosts) != 1 || ingress.Spec.TLS[0].Hosts[0] != host {
			ingress.Spec.TLS[0].Hosts = []string{host}
			changed = true
		}
	}

	return changed
}

// Helper function to compare maps for equality
func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for key, valueA := range a {
		if valueB, exists := b[key]; !exists || valueA != valueB {
			return false
		}
	}

	return true
}
