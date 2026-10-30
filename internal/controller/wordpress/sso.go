package wordpress

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	crmv1 "hostzero.de/m/v2/api/v1"
)

// Admin SSO: an opt-in must-use plugin that logs a user in from a
// short-lived token signed with a per-site key. The key lives in the site
// Secret so an external control plane can issue tokens; the plugin source
// is shipped through the site's ConfigMap and copied into
// wp-content/mu-plugins by the init container on every start.
const (
	// SSOSecretKey is the key in the site Secret holding the HMAC secret
	SSOSecretKey = "ssoKey"
	// SSOPluginKey is the ConfigMap key holding the plugin source
	SSOPluginKey = "kubepress-sso.php"
	// SSOPluginMountPath is where the init container finds the plugin source
	SSOPluginMountPath = "/kubepress/kubepress-sso.php"
	// SSOEnabledEnv tells the init container whether to install or remove the plugin
	SSOEnabledEnv = "KUBEPRESS_SSO_ENABLED"
	// SSOKeyEnv exposes the key to PHP inside the WordPress container
	SSOKeyEnv = "KUBEPRESS_SSO_KEY"
	// ssoKeyBytes is the key length; 32 bytes = 64 hex characters
	ssoKeyBytes = 32
)

//go:embed assets/kubepress-sso.php
var ssoPluginSource string

// SSOPluginSource returns the must-use plugin shipped to every site
func SSOPluginSource() string {
	return ssoPluginSource
}

// adminSSOEnabled returns whether token login is enabled for the site (default false)
func adminSSOEnabled(wp *crmv1.WordPressSite) bool {
	return wp.Spec.WordPress.AdminSSO != nil && wp.Spec.WordPress.AdminSSO.Enabled
}

// ReconcileSSOKey makes sure the site Secret carries an SSO key when the
// feature is enabled. An existing key is never replaced; rotate it by
// deleting the key from the Secret.
func ReconcileSSOKey(ctx context.Context, r client.Client, wp *crmv1.WordPressSite) error {
	if !adminSSOEnabled(wp) {
		return nil
	}
	logger := log.FromContext(ctx).WithValues("component", "sso")

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{Name: GetDatabaseSecretName(wp.Name), Namespace: wp.Namespace}, secret); err != nil {
		logger.Error(err, "Failed to get site secret for SSO key")
		return err
	}
	if len(secret.Data[SSOSecretKey]) > 0 {
		return nil
	}

	key, err := generateSSOKey()
	if err != nil {
		logger.Error(err, "Failed to generate SSO key")
		return err
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data[SSOSecretKey] = []byte(key)
	if err := r.Update(ctx, secret); err != nil {
		logger.Error(err, "Failed to store SSO key in site secret")
		return err
	}
	logger.Info("Generated admin SSO key", "secret", secret.Name)
	return nil
}

func generateSSOKey() (string, error) {
	b := make([]byte, ssoKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random source failed: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ssoPluginVolumeMount mounts the plugin source from the php-config
// ConfigMap into the init container
func ssoPluginVolumeMount() corev1.VolumeMount {
	return corev1.VolumeMount{
		Name:      "php-config",
		MountPath: SSOPluginMountPath,
		SubPath:   SSOPluginKey,
	}
}

// ssoInitEnv is the init container flag that installs or removes the plugin
func ssoInitEnv(wp *crmv1.WordPressSite) corev1.EnvVar {
	return corev1.EnvVar{Name: SSOEnabledEnv, Value: fmt.Sprintf("%t", adminSSOEnabled(wp))}
}

// ssoKeyEnv is the WordPress container env var carrying the key
func ssoKeyEnv(wp *crmv1.WordPressSite) corev1.EnvVar {
	return corev1.EnvVar{
		Name: SSOKeyEnv,
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: wp.Spec.AdminUserSecretKeyRef},
			Key:                  SSOSecretKey,
		}},
	}
}

// ssoInitScript is appended to the init container script. It installs the
// plugin when enabled and removes it otherwise, so toggling the flag off
// leaves nothing behind.
const ssoInitScript = `
# KubePress admin SSO must-use plugin (managed by the operator)
if [ "$` + SSOEnabledEnv + `" = "true" ] && [ -f ` + SSOPluginMountPath + ` ]; then
	mkdir -p /var/www/html/wp-content/mu-plugins
	cp ` + SSOPluginMountPath + ` /var/www/html/wp-content/mu-plugins/` + SSOPluginKey + `
else
	rm -f /var/www/html/wp-content/mu-plugins/` + SSOPluginKey + `
fi
`

// setEnvVar sets or replaces an env var by name; returns true when changed
func setEnvVar(env *[]corev1.EnvVar, desired corev1.EnvVar) bool {
	for i, e := range *env {
		if e.Name == desired.Name {
			if e.Value == desired.Value && envSourceEqual(e.ValueFrom, desired.ValueFrom) {
				return false
			}
			(*env)[i] = desired
			return true
		}
	}
	*env = append(*env, desired)
	return true
}

// removeEnvVar drops an env var by name; returns true when it was present
func removeEnvVar(env *[]corev1.EnvVar, name string) bool {
	for i, e := range *env {
		if e.Name == name {
			*env = append((*env)[:i], (*env)[i+1:]...)
			return true
		}
	}
	return false
}

func envSourceEqual(a, b *corev1.EnvVarSource) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.SecretKeyRef == nil || b.SecretKeyRef == nil {
		return a.SecretKeyRef == b.SecretKeyRef
	}
	return a.SecretKeyRef.Name == b.SecretKeyRef.Name && a.SecretKeyRef.Key == b.SecretKeyRef.Key
}

// reconcileSSOContainers brings the init and WordPress containers of an
// existing deployment in line with the AdminSSO setting. Returns true if
// the deployment was modified.
func reconcileSSOContainers(podSpec *corev1.PodSpec, wp *crmv1.WordPressSite) bool {
	changed := false
	init := &podSpec.InitContainers[0]
	web := &podSpec.Containers[0]

	// plugin source mount on the init container
	hasMount := false
	for _, m := range init.VolumeMounts {
		if m.MountPath == SSOPluginMountPath {
			hasMount = true
			break
		}
	}
	if !hasMount {
		init.VolumeMounts = append(init.VolumeMounts, ssoPluginVolumeMount())
		changed = true
	}

	if setEnvVar(&init.Env, ssoInitEnv(wp)) {
		changed = true
	}

	if adminSSOEnabled(wp) {
		if setEnvVar(&web.Env, ssoKeyEnv(wp)) {
			changed = true
		}
	} else if removeEnvVar(&web.Env, SSOKeyEnv) {
		changed = true
	}

	return changed
}
