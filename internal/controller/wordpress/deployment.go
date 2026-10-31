package wordpress

import (
	"context"
	"fmt"
	crmv1 "hostzero.de/m/v2/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"os"
	"reflect"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	DefaultVolumeName = "wordpress-central-data"
)

// ReconcileDeployment creates or updates the Deployment for WordPress
func ReconcileDeployment(ctx context.Context, r client.Client, scheme *runtime.Scheme, wp *crmv1.WordPressSite) error {
	logger := log.FromContext(ctx).WithValues("component", "ingress")

	logger = logger.WithValues("component", "deployment", "site", wp.Name, "namespace", wp.Namespace)

	deploymentName := GetResourceName(wp.Name)

	// Check if deployment exists
	deployment := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: wp.Namespace}, deployment)

	if errors.IsNotFound(err) {
		// Create new deployment
		// check if MySQL secret exists
		mySQLSecretName := wp.Spec.AdminUserSecretKeyRef

		replicas := int32(1)
		if wp.Spec.WordPress.Replicas > 0 {
			replicas = wp.Spec.WordPress.Replicas
		}

		labels := GetWordpressLabels(wp, map[string]string{
			"app.kubernetes.io/name": "wordpress-server",
		})
		labelsForMatching := GetWordpressLabelsForMatching(wp, map[string]string{
			"app.kubernetes.io/name": "wordpress-server",
		})

		// Configure volume mounts with subpath
		volumeMounts := []corev1.VolumeMount{
			{
				Name:      DefaultVolumeName,
				MountPath: "/var/www/html",
			},
			{
				Name:      "php-config",
				MountPath: "/usr/local/etc/php/conf.d/custom.ini",
				SubPath:   "php.ini",
			},
		}

		// Configure volumes to use central PVC
		volumes := []corev1.Volume{
			{
				Name: DefaultVolumeName,
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: GetPVCName(wp.Name),
					},
				},
			},
			{
				Name: "php-config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: GetConfigMapName(wp.Name),
						},
					},
				},
			},
		}

		// convert memory limit from Go format to PHP format
		memoryLimit, err := GoMemoryToPHPMemory(wp.Spec.WordPress.Resources.MemoryLimit)
		if err != nil {
			logger.Error(err, "Failed to convert memory limit", "value", wp.Spec.WordPress.Resources.MemoryLimit)
			return err
		}

		// Add initContainer for WordPress
		// Init Container is the best solution to ensure WordPress is properly installed
		// PostStart hooks don't allow logging and are not suitable for complex initialization
		// Init Containers are run before the main container and can handle a complex setup
		// Sadly we also have to do the wp installation here, because we need an installed WordPress to configure it
		// We can also not use the cli image variant from the official WordPress image, because it uses alpine linux and thus all the data that is written will not be readable by the main container
		// This init container runs WordPress setup as www-data user for security best practices
		// IMPORTANT: Never use root user - all operations run as www-data (uid 33)
		// The FSGroup in PodSecurityContext ensures volume ownership is set correctly
		initContainer := corev1.Container{
			Name:  "init",
			Image: wp.Spec.WordPress.Image,
			//SecurityContext: &corev1.SecurityContext{
			//	// Run as www-data user for security
			//	RunAsUser:                &[]int64{33}[0],
			//	RunAsGroup:               &[]int64{33}[0],
			//	RunAsNonRoot:             &[]bool{true}[0],
			//	AllowPrivilegeEscalation: &[]bool{false}[0],
			//	ReadOnlyRootFilesystem:   &[]bool{false}[0], // WordPress needs write access
			//	Capabilities: &corev1.Capabilities{
			//		Drop: []corev1.Capability{"ALL"},
			//		Add:  []corev1.Capability{"CHOWN", "SETUID", "SETGID"}, // Minimal capabilities
			//	},
			//},
			Command: []string{"sh", "-c", `#!/bin/bash
set -e

# Ensure we're running as www-data user
#[ "$(id -u)" != "33" ] && { echo "ERROR: Must run as www-data user (uid 33)"; exit 1; }

# Check volume accessibility
#[ ! -d "/var/www/html" ] && { echo "ERROR: /var/www/html directory does not exist"; exit 1; }
#touch /var/www/html/.test-write 2>/dev/null || { echo "ERROR: Cannot write to /var/www/html"; exit 1; }
#rm -f /var/www/html/.test-write

# Ensure wp-cli is installed
if [ ! -f /tmp/wp-cli ]; then
	curl -s -O https://raw.githubusercontent.com/wp-cli/builds/gh-pages/phar/wp-cli.phar >/dev/null 2>&1
	chmod +x wp-cli.phar >/dev/null 2>&1
	mv wp-cli.phar /tmp/wp-cli
fi

if [ ! -f /var/www/html/index.php ]; then
	echo "Downloading WordPress core files..."
	/tmp/wp-cli core download --path="/var/www/html/" --locale=en_US --allow-root
fi

# Create wp-config.php if it doesn't exist
if [ ! -f /var/www/html/wp-config.php ]; then
	echo "Creating wp-config.php..."
	/tmp/wp-cli config create --path="/var/www/html/" \
		--dbhost="$WORDPRESS_DB_HOST" \
		--dbname="$WORDPRESS_DB_NAME" \
		--dbuser="$WORDPRESS_DB_USER" \
		--dbpass="$WORDPRESS_DB_PASSWORD" \
    	--allow-root \
		--extra-php <<PHP
define('FS_METHOD', 'direct');
define('WP_MEMORY_LIMIT', '256M');
PHP

	# add TLS workaround if behind a proxy
	sed -i '2 i define('\''FORCE_SSL_ADMIN'\'', true); if ($_SERVER["HTTP_X_FORWARDED_PROTO"] == "https") $_SERVER["HTTPS"]="on";' /var/www/html/wp-config.php
fi

# Set the WP Memory Limit correctly
/tmp/wp-cli config set WP_MEMORY_LIMIT "$WORDPRESS_MEMORY_LIMIT" --path="/var/www/html/" --allow-root

# Install WordPress if not already installed
if ! /tmp/wp-cli core is-installed --path="/var/www/html/" --quiet 2>/dev/null; then
	/tmp/wp-cli core install \
		--path="/var/www/html/" \
		--url="$WORDPRESS_URL" \
		--title="$WORDPRESS_TITLE" \
		--admin_user="$WORDPRESS_ADMIN_USER" \
		--admin_password="$WORDPRESS_ADMIN_PASSWORD" \
		--admin_email="$WORDPRESS_ADMIN_EMAIL" \
		--skip-email \
		--allow-root
fi

if [ "${KUBEPRESS_SYNC_WP_CONTENT:-false}" = "true" ]; then
	for subdir in themes plugins mu-plugins; do
		src="/usr/src/wordpress/wp-content/${subdir}"
		dst="/var/www/html/wp-content/${subdir}"
		if [ -d "$src" ]; then
			echo "Syncing ${subdir} from image into WordPress content volume..."
			mkdir -p "$dst"
			cp -a "$src/." "$dst/"
		fi
	done
fi

# Set proper ownership and permissions
chown -R 33:33 /var/www/html
`},
			VolumeMounts: volumeMounts, // share volumes with main container if needed
			Env:          desiredInitEnvVars(wp, mySQLSecretName, memoryLimit),
		}
		initContainer.Env = append(initContainer.Env, toCoreEnvVars(wp.Spec.WordPress.Env)...)

		// Create Pod specification
		podSpec := corev1.PodSpec{
			//SecurityContext: &corev1.PodSecurityContext{
			//	// Run as www-data user and group
			//	RunAsUser:    &[]int64{33}[0],
			//	RunAsGroup:   &[]int64{33}[0],
			//	RunAsNonRoot: &[]bool{true}[0],
			//	FSGroup:      &[]int64{33}[0],
			//	// Always change ownership to ensure www-data can write to volumes
			//	FSGroupChangePolicy: &[]corev1.PodFSGroupChangePolicy{corev1.FSGroupChangeAlways}[0],
			//
			//	SeccompProfile: &corev1.SeccompProfile{
			//		Type: corev1.SeccompProfileTypeRuntimeDefault,
			//	},
			//	// Additional security measures
			//	SupplementalGroups: []int64{33}, // www-data group
			//},
			InitContainers: []corev1.Container{
				initContainer,
			},
			Containers: []corev1.Container{
				{
					Name:  "wordpress",
					Image: wp.Spec.WordPress.Image,
					//SecurityContext: &corev1.SecurityContext{
					//	// Run as www-data user for security
					//	RunAsUser:                &[]int64{33}[0],
					//	RunAsGroup:               &[]int64{33}[0],
					//	RunAsNonRoot:             &[]bool{true}[0],
					//	AllowPrivilegeEscalation: &[]bool{false}[0],
					//	ReadOnlyRootFilesystem:   &[]bool{false}[0], // WordPress needs write access
					//	Capabilities: &corev1.Capabilities{
					//		Drop: []corev1.Capability{"ALL"},
					//		Add:  []corev1.Capability{"CHOWN", "SETUID", "SETGID"}, // Minimal capabilities
					//	},
					//},
					Env: append(desiredWordPressContainerEnvVars(mySQLSecretName), []corev1.EnvVar{
						{
							Name:  "WORDPRESS_TABLE_PREFIX",
							Value: "wp_",
						},
						{
							Name:  "APACHE_RUN_USER",
							Value: "www-data",
						},
						{
							Name:  "APACHE_RUN_GROUP",
							Value: "www-data",
						},
					}...),
					Ports: []corev1.ContainerPort{
						{
							Name:          "http",
							ContainerPort: 80,
						},
					},
					VolumeMounts: volumeMounts,
					Resources:    buildWordPressResources(wp),
				},
			},
			Volumes: volumes,
		}
		podSpec.Containers[0].Env = append(podSpec.Containers[0].Env, toCoreEnvVars(wp.Spec.WordPress.Env)...)
		if imagePullSecret := resolveImagePullSecret(wp); imagePullSecret != "" {
			podSpec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: imagePullSecret}}
		}

		// Create the deployment
		deployment = &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deploymentName,
				Namespace: wp.Namespace,
				Labels:    labels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: labelsForMatching,
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: labels,
					},
					Spec: podSpec,
				},
			},
		}

		// Set owner reference
		if err := controllerutil.SetControllerReference(wp, deployment, scheme); err != nil {
			logger.Error(err, "Unable to set owner reference to Deployment", "object", deployment.GetName())
			return err
		}

		if err := r.Create(ctx, deployment); err != nil {
			logger.Error(err, "Failed to create WordPress deployment")
			return fmt.Errorf("failed to create deployment %s: %w", deploymentName, err)
		}

	} else if err != nil {
		logger.Error(err, "Failed to get WordPress deployment")
		return fmt.Errorf("failed to get deployment %s: %w", deploymentName, err)
	} else {
		// Update existing deployment if needed
		updateNeeded := false

		// Check if image needs to be updated
		if deployment.Spec.Template.Spec.Containers[0].Image != wp.Spec.WordPress.Image {
			deployment.Spec.Template.Spec.Containers[0].Image = wp.Spec.WordPress.Image
			updateNeeded = true
		}
		if len(deployment.Spec.Template.Spec.InitContainers) > 0 && deployment.Spec.Template.Spec.InitContainers[0].Image != wp.Spec.WordPress.Image {
			deployment.Spec.Template.Spec.InitContainers[0].Image = wp.Spec.WordPress.Image
			updateNeeded = true
		}

		// Check if replicas need to be updated
		if *deployment.Spec.Replicas != wp.Spec.WordPress.Replicas {
			deployment.Spec.Replicas = &wp.Spec.WordPress.Replicas
			updateNeeded = true
		}

		// Add environment variables handling
		if len(wp.Spec.WordPress.Env) > 0 {
			if envVarsChanged := updateEnvVars(&deployment.Spec.Template.Spec.Containers[0].Env, wp.Spec.WordPress.Env); envVarsChanged {
				updateNeeded = true
			}
			if len(deployment.Spec.Template.Spec.InitContainers) > 0 {
				if envVarsChanged := updateEnvVars(&deployment.Spec.Template.Spec.InitContainers[0].Env, wp.Spec.WordPress.Env); envVarsChanged {
					updateNeeded = true
				}
			}
		}

		mySQLSecretName := wp.Spec.AdminUserSecretKeyRef
		for _, envVar := range desiredWordPressContainerEnvVars(mySQLSecretName) {
			if upsertEnvVar(&deployment.Spec.Template.Spec.Containers[0].Env, envVar) {
				updateNeeded = true
			}
		}

		if imagePullSecret := resolveImagePullSecret(wp); imagePullSecret != "" && !imagePullSecretsEqual(deployment.Spec.Template.Spec.ImagePullSecrets, imagePullSecret) {
			deployment.Spec.Template.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: imagePullSecret}}
			updateNeeded = true
		}

		if imagePullSecret := resolveImagePullSecret(wp); imagePullSecret == "" && len(deployment.Spec.Template.Spec.ImagePullSecrets) > 0 {
			deployment.Spec.Template.Spec.ImagePullSecrets = nil
			updateNeeded = true
		}

		// Check if resource requests/limits need to be updated
		resources := buildWordPressResources(wp)

		if !resourcesEqual(deployment.Spec.Template.Spec.Containers[0].Resources, resources) {
			deployment.Spec.Template.Spec.Containers[0].Resources = resources
			updateNeeded = true
		}

		// convert memory limit from Go format to PHP format
		memoryLimit, err := GoMemoryToPHPMemory(wp.Spec.WordPress.Resources.MemoryLimit)
		if err != nil {
			logger.Error(err, "Failed to convert memory limit", "value", wp.Spec.WordPress.Resources.MemoryLimit)
			return err
		}

		if len(deployment.Spec.Template.Spec.InitContainers) > 0 {
			for _, envVar := range desiredInitEnvVars(wp, mySQLSecretName, memoryLimit) {
				if upsertEnvVar(&deployment.Spec.Template.Spec.InitContainers[0].Env, envVar) {
					updateNeeded = true
				}
			}
		}

		// Update the deployment if needed
		if updateNeeded {
			logger.Info("Updating WordPress deployment")
			if err := r.Update(ctx, deployment); err != nil {
				logger.Error(err, "Failed to update WordPress deployment")
				return fmt.Errorf("failed to update deployment %s: %w", deploymentName, err)
			}
		}
	}

	return nil
}

func desiredWordPressContainerEnvVars(secretName string) []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "WORDPRESS_DB_HOST", ValueFrom: secretKeyEnv(secretName, "databaseHost")},
		{Name: "WORDPRESS_DB_NAME", ValueFrom: secretKeyEnv(secretName, "database")},
		{Name: "WORDPRESS_DB_USER", ValueFrom: secretKeyEnv(secretName, "databaseUsername")},
		{Name: "WORDPRESS_DB_PASSWORD", ValueFrom: secretKeyEnv(secretName, "databasePassword")},
	}
}

func desiredInitEnvVars(wp *crmv1.WordPressSite, secretName, memoryLimit string) []corev1.EnvVar {
	return append(desiredWordPressContainerEnvVars(secretName), []corev1.EnvVar{
		{Name: "WORDPRESS_URL", Value: getSiteUrl(wp)},
		{Name: "WORDPRESS_TITLE", Value: wp.Spec.SiteTitle},
		{Name: "WORDPRESS_ADMIN_USER", ValueFrom: secretKeyEnv(secretName, "username")},
		{Name: "WORDPRESS_ADMIN_PASSWORD", ValueFrom: secretKeyEnv(secretName, "password")},
		{Name: "WORDPRESS_ADMIN_EMAIL", Value: wp.Spec.AdminEmail},
		{Name: "WORDPRESS_MEMORY_LIMIT", Value: memoryLimit},
	}...)
}

func secretKeyEnv(secretName, key string) *corev1.EnvVarSource {
	return &corev1.EnvVarSource{
		SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
			Key:                  key,
		},
	}
}

func buildWordPressResources(wp *crmv1.WordPressSite) corev1.ResourceRequirements {
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{},
		Limits:   corev1.ResourceList{},
	}

	if wp.Spec.WordPress.Resources == nil {
		return resources
	}

	if wp.Spec.WordPress.Resources.CPURequest != "" {
		resources.Requests[corev1.ResourceCPU] = resource.MustParse(wp.Spec.WordPress.Resources.CPURequest)
	}
	if wp.Spec.WordPress.Resources.CPULimit != "" {
		resources.Limits[corev1.ResourceCPU] = resource.MustParse(wp.Spec.WordPress.Resources.CPULimit)
	}
	if wp.Spec.WordPress.Resources.MemoryRequest != "" {
		resources.Requests[corev1.ResourceMemory] = resource.MustParse(wp.Spec.WordPress.Resources.MemoryRequest)
	}
	if wp.Spec.WordPress.Resources.MemoryLimit != "" {
		resources.Limits[corev1.ResourceMemory] = resource.MustParse(wp.Spec.WordPress.Resources.MemoryLimit)
	}

	return resources
}

func resolveImagePullSecret(wp *crmv1.WordPressSite) string {
	if wp.Spec.WordPress.ImagePullSecret != "" {
		return wp.Spec.WordPress.ImagePullSecret
	}
	return os.Getenv("WORDPRESS_IMAGE_PULL_SECRET")
}

func imagePullSecretsEqual(actual []corev1.LocalObjectReference, expected string) bool {
	return len(actual) == 1 && actual[0].Name == expected
}

// toCoreEnvVars converts custom environment variables, skipping the ones the operator manages.
func toCoreEnvVars(envVars []crmv1.EnvVar) []corev1.EnvVar {
	coreEnvVars := make([]corev1.EnvVar, 0, len(envVars))
	for _, env := range envVars {
		if isManagedEnvVar(env.Name) {
			continue
		}
		coreEnvVars = append(coreEnvVars, corev1.EnvVar{
			Name:  env.Name,
			Value: env.Value,
		})
	}
	return coreEnvVars
}

func resourcesEqual(requirements corev1.ResourceRequirements, actual corev1.ResourceRequirements) bool {
	if len(requirements.Limits) != len(actual.Limits) || len(requirements.Requests) != len(actual.Requests) {
		return false
	}

	for name, quantity := range requirements.Limits {
		if actualQuantity, exists := actual.Limits[name]; !exists || actualQuantity.Cmp(quantity) != 0 {
			return false
		}
	}

	for name, quantity := range requirements.Requests {
		if actualQuantity, exists := actual.Requests[name]; !exists || actualQuantity.Cmp(quantity) != 0 {
			return false
		}
	}

	return true

}

// updateEnvVars updates custom environment variables in a container.
func updateEnvVars(containerEnv *[]corev1.EnvVar, envVars []crmv1.EnvVar) bool {
	changed := false
	existingEnvVars := make(map[string]int)
	for i, env := range *containerEnv {
		existingEnvVars[env.Name] = i
	}

	for _, env := range envVars {
		if isManagedEnvVar(env.Name) {
			continue
		}

		if existingIndex, ok := existingEnvVars[env.Name]; ok {
			if (*containerEnv)[existingIndex].Value != env.Value {
				(*containerEnv)[existingIndex].Value = env.Value
				changed = true
			}
			continue
		}

		*containerEnv = append(
			*containerEnv,
			corev1.EnvVar{
				Name:  env.Name,
				Value: env.Value,
			},
		)
		changed = true
	}

	return changed
}

func isManagedEnvVar(name string) bool {
	switch name {
	case "WORDPRESS_DB_HOST",
		"WORDPRESS_DB_NAME",
		"WORDPRESS_DB_USER",
		"WORDPRESS_DB_PASSWORD",
		"WORDPRESS_TABLE_PREFIX",
		"APACHE_RUN_USER",
		"APACHE_RUN_GROUP",
		"WORDPRESS_URL",
		"WORDPRESS_TITLE",
		"WORDPRESS_ADMIN_USER",
		"WORDPRESS_ADMIN_PASSWORD",
		"WORDPRESS_ADMIN_EMAIL",
		"WORDPRESS_MEMORY_LIMIT":
		return true
	default:
		return false
	}
}

func upsertEnvVar(containerEnv *[]corev1.EnvVar, desired corev1.EnvVar) bool {
	for i, env := range *containerEnv {
		if env.Name != desired.Name {
			continue
		}
		if reflect.DeepEqual(env, desired) {
			return false
		}
		(*containerEnv)[i] = desired
		return true
	}

	*containerEnv = append(*containerEnv, desired)
	return true
}

// Helper functions to get values from the WordPress spec
func getSiteUrl(wp *crmv1.WordPressSite) string {
	if wp.Spec.Ingress != nil && wp.Spec.Ingress.Host != "" {
		protocol := "http"
		if wp.Spec.Ingress.TLS {
			protocol = "https"
		}
		return fmt.Sprintf("%s://%s", protocol, wp.Spec.Ingress.Host)
	}
	return fmt.Sprintf("http://%s", wp.Name)
}
