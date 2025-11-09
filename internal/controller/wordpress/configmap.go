package wordpress

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	crmv1 "hostzero.de/m/v2/api/v1"
)

// ReconcileConfigMap creates or updates the ConfigMap for WordPress PHP configuration
func ReconcileConfigMap(ctx context.Context, r client.Client, scheme *runtime.Scheme, wp *crmv1.WordPressSite) error {
	logger := log.FromContext(ctx).WithValues("component", "configmap")

	labels := GetWordpressLabels(wp, map[string]string{
		"app.kubernetes.io/name": "php-config",
	})

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        GetConfigMapName(wp.Name),
			Namespace:   wp.Namespace,
			Labels:      labels,
			Annotations: map[string]string{},
		},
	}

	// convert memory limit from Go format to PHP format
	memoryLimit, err := GoMemoryToPHPMemory(wp.Spec.WordPress.Resources.MemoryLimit)
	if err != nil {
		logger.Error(err, "Failed to convert memory limit", "value", wp.Spec.WordPress.Resources.MemoryLimit)
		return err
	}

	maxUploadLimit := wp.Spec.WordPress.MaxUploadLimit
	if maxUploadLimit == "" {
		maxUploadLimit = "64M" // default value if not set
	}

	// PHP configuration with reasonable defaults
	phpConfig := map[string]string{
		"upload_max_filesize":      maxUploadLimit, // Maximum size for uploaded files (affects media uploads, theme/plugin uploads)
		"post_max_size":            maxUploadLimit, // Maximum size of POST data (must be larger than upload_max_filesize)
		"memory_limit":             memoryLimit,    // Maximum memory a script can consume (WordPress core needs ~40MB, plugins may need more)
		"max_execution_time":       "60",           // Maximum time in seconds a script is allowed to run (0 = no limit)
		"max_input_vars":           "3000",         // Maximum number of input variables accepted (affects forms with many fields)
		"session.cookie_httponly":  "1",            // Prevents JavaScript access to session cookies (XSS protection)
		"session.cookie_secure":    "1",            // Only send session cookies over HTTPS connections
		"session.use_only_cookies": "1",            // Use only cookies for session ID (prevents session fixation attacks)
	}

	// Override with user-specified PHP config
	if len(wp.Spec.WordPress.PHPConfig) > 0 {
		for k, v := range wp.Spec.WordPress.PHPConfig {
			phpConfig[k] = v
		}
	}

	// Build PHP ini content deterministically
	keys := make([]string, 0, len(phpConfig))
	for k := range phpConfig {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	phpIniContent := ""
	for _, k := range keys {
		phpIniContent += fmt.Sprintf("%s = %s\n", k, phpConfig[k])
	}

	// Compare with existing config (if any)
	existingConfigMap := &corev1.ConfigMap{}
	configChanged := false
	err = r.Get(ctx, client.ObjectKey{Name: configMap.Name, Namespace: configMap.Namespace}, existingConfigMap)

	if err != nil && !errors.IsNotFound(err) {
		// log the error and return
		logger.Error(err, "Failed to get existing ConfigMap", "name", configMap.Name)
		return err
	}

	if err == nil {
		existing, ok := existingConfigMap.Data["php.ini"]
		if ok && existing != phpIniContent {
			configChanged = true
		}
	}

	// Create or update the ConfigMap
	_, err = ctrl.CreateOrUpdate(ctx, r, configMap, func() error {
		configMap.Data = map[string]string{
			"php.ini": phpIniContent,
		}

		return controllerutil.SetControllerReference(wp, configMap, scheme)
	})

	if err != nil {
		logger.Error(err, "Failed to reconcile ConfigMap", "name", configMap.Name)
		return err
	}

	// If config changed, rollout restart of Deployment
	if configChanged {
		deploymentName := GetResourceName(wp.Name)
		deployment := &appsv1.Deployment{}
		err := r.Get(ctx, client.ObjectKey{Name: deploymentName, Namespace: wp.Namespace}, deployment)
		if err == nil {
			if deployment.Spec.Template.Annotations == nil {
				deployment.Spec.Template.Annotations = map[string]string{}
			}
			deployment.Spec.Template.Annotations["kubepress.io/restarted-at"] = fmt.Sprintf("%d", metav1.Now().Unix())
			err = r.Update(ctx, deployment)
			if err != nil {
				logger.Error(err, "Failed to annotate Deployment for restart", "name", deploymentName)
				return err
			}
		}
	}

	return nil
}
