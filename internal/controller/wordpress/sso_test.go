package wordpress

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	crmv1 "hostzero.de/m/v2/api/v1"
)

func ssoSite(enabled bool) *crmv1.WordPressSite {
	wp := &crmv1.WordPressSite{}
	wp.Name = "wp--example"
	wp.Spec.AdminUserSecretKeyRef = "wp--example"
	if enabled {
		wp.Spec.WordPress.AdminSSO = &crmv1.AdminSSOConfig{Enabled: true}
	}
	return wp
}

func TestAdminSSODisabledByDefault(t *testing.T) {
	if adminSSOEnabled(ssoSite(false)) {
		t.Fatal("adminSSO must be opt-in")
	}
	if !adminSSOEnabled(ssoSite(true)) {
		t.Fatal("adminSSO enabled flag not honoured")
	}
}

func TestSSOPluginSourceEmbedded(t *testing.T) {
	src := SSOPluginSource()
	if !strings.HasPrefix(src, "<?php") || !strings.Contains(src, "Plugin Name: KubePress Admin SSO") {
		t.Fatalf("unexpected plugin source: %.60q", src)
	}
	if !strings.Contains(src, "getenv('"+SSOKeyEnv+"')") {
		t.Fatalf("plugin does not read %s", SSOKeyEnv)
	}
}

func TestInitScriptInstallsAndRemovesPlugin(t *testing.T) {
	script := initContainerScript
	for _, want := range []string{SSOEnabledEnv, SSOPluginMountPath, "mu-plugins/" + SSOPluginKey, "rm -f"} {
		if !strings.Contains(script, want) {
			t.Errorf("init script lacks %q", want)
		}
	}
	if strings.Index(script, "mu-plugins") > strings.Index(script, "chown -R 33:33") {
		t.Error("plugin must be installed before ownership is fixed")
	}
}

func TestGenerateSSOKey(t *testing.T) {
	a, err := generateSSOKey()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := generateSSOKey()
	if len(a) != ssoKeyBytes*2 || a == b {
		t.Fatalf("bad key %q", a)
	}
}

func TestReconcileSSOContainers(t *testing.T) {
	podSpec := &corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "init", Env: []corev1.EnvVar{{Name: "WORDPRESS_MEMORY_LIMIT", Value: "1024M"}}}},
		Containers:     []corev1.Container{{Name: "wordpress", Env: []corev1.EnvVar{{Name: "WORDPRESS_TABLE_PREFIX", Value: "wp_"}}}},
	}

	// enable on an existing deployment
	if !reconcileSSOContainers(podSpec, ssoSite(true)) {
		t.Fatal("expected change when enabling")
	}
	if len(podSpec.InitContainers[0].VolumeMounts) != 1 || podSpec.InitContainers[0].VolumeMounts[0].MountPath != SSOPluginMountPath {
		t.Fatal("plugin mount missing on init container")
	}
	if got := findEnv(podSpec.InitContainers[0].Env, SSOEnabledEnv); got == nil || got.Value != "true" {
		t.Fatalf("init flag = %v", got)
	}
	if got := findEnv(podSpec.Containers[0].Env, SSOKeyEnv); got == nil || got.ValueFrom.SecretKeyRef.Key != SSOSecretKey || got.ValueFrom.SecretKeyRef.Name != "wp--example" {
		t.Fatalf("key env = %v", got)
	}
	if reconcileSSOContainers(podSpec, ssoSite(true)) {
		t.Fatal("second reconcile must be a no-op")
	}

	// disable again
	if !reconcileSSOContainers(podSpec, ssoSite(false)) {
		t.Fatal("expected change when disabling")
	}
	if got := findEnv(podSpec.InitContainers[0].Env, SSOEnabledEnv); got == nil || got.Value != "false" {
		t.Fatalf("init flag after disable = %v", got)
	}
	if findEnv(podSpec.Containers[0].Env, SSOKeyEnv) != nil {
		t.Fatal("key env must be removed when disabled")
	}
	if findEnv(podSpec.Containers[0].Env, "WORDPRESS_TABLE_PREFIX") == nil {
		t.Fatal("unrelated env vars must survive")
	}
}

func findEnv(env []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range env {
		if env[i].Name == name {
			return &env[i]
		}
	}
	return nil
}
