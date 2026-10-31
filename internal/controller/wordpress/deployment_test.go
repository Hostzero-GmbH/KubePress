package wordpress

import (
	"context"
	"testing"

	crmv1 "hostzero.de/m/v2/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestReconcileDeploymentUpdatesBuiltInEnvVars(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := crmv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	wp := &crmv1.WordPressSite{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example",
			Namespace: "default",
			UID:       types.UID("7bce6ded-8660-4ae6-8c32-a3c93f42a715"),
		},
		Spec: crmv1.WordPressSiteSpec{
			SiteTitle:             "New title",
			AdminEmail:            "new-admin@example.com",
			AdminUserSecretKeyRef: "new-secret",
			Ingress: &crmv1.IngressConfig{
				Enabled: true,
				Host:    "new.example.com",
				TLS:     true,
			},
			WordPress: crmv1.WordPressConfig{
				Image:    "wordpress:new",
				Replicas: 1,
				Resources: &crmv1.ResourceRequirements{
					CPULimit:      "1000m",
					CPURequest:    "250m",
					MemoryLimit:   "2Gi",
					MemoryRequest: "512Mi",
				},
			},
		},
	}

	replicas := int32(1)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example",
			Namespace: "default",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					InitContainers: []corev1.Container{{
						Name:  "init",
						Image: "wordpress:old",
						Env: []corev1.EnvVar{
							{Name: "WORDPRESS_URL", Value: "https://old.example.com"},
							{Name: "WORDPRESS_TITLE", Value: "Old title"},
							{Name: "WORDPRESS_ADMIN_EMAIL", Value: "old-admin@example.com"},
							{Name: "WORDPRESS_MEMORY_LIMIT", Value: "512M"},
							{Name: "WORDPRESS_ADMIN_USER", ValueFrom: secretKeyEnv("old-secret", "username")},
						},
					}},
					Containers: []corev1.Container{{
						Name:  "wordpress",
						Image: "wordpress:old",
						Env: []corev1.EnvVar{
							{Name: "WORDPRESS_DB_HOST", ValueFrom: secretKeyEnv("old-secret", "databaseHost")},
						},
					}},
				},
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(deployment).Build()
	if err := ReconcileDeployment(ctx, client, scheme, wp); err != nil {
		t.Fatalf("ReconcileDeployment returned error: %v", err)
	}

	var got appsv1.Deployment
	if err := client.Get(ctx, types.NamespacedName{Name: "example", Namespace: "default"}, &got); err != nil {
		t.Fatalf("failed to get deployment: %v", err)
	}

	initContainer := got.Spec.Template.Spec.InitContainers[0]
	if initContainer.Image != "wordpress:new" {
		t.Fatalf("init image = %q, want wordpress:new", initContainer.Image)
	}
	assertEnvValue(t, initContainer.Env, "WORDPRESS_URL", "https://new.example.com")
	assertEnvValue(t, initContainer.Env, "WORDPRESS_TITLE", "New title")
	assertEnvValue(t, initContainer.Env, "WORDPRESS_ADMIN_EMAIL", "new-admin@example.com")
	assertEnvValue(t, initContainer.Env, "WORDPRESS_MEMORY_LIMIT", "2048M")
	assertSecretRef(t, initContainer.Env, "WORDPRESS_ADMIN_USER", "new-secret", "username")
	assertSecretRef(t, initContainer.Env, "WORDPRESS_ADMIN_PASSWORD", "new-secret", "password")

	containerEnv := got.Spec.Template.Spec.Containers[0].Env
	assertSecretRef(t, containerEnv, "WORDPRESS_DB_HOST", "new-secret", "databaseHost")
	assertSecretRef(t, containerEnv, "WORDPRESS_DB_NAME", "new-secret", "database")
	assertSecretRef(t, containerEnv, "WORDPRESS_DB_USER", "new-secret", "databaseUsername")
	assertSecretRef(t, containerEnv, "WORDPRESS_DB_PASSWORD", "new-secret", "databasePassword")
}

func TestUpdateEnvVarsSkipsManagedEnvVars(t *testing.T) {
	env := []corev1.EnvVar{
		{Name: "WORDPRESS_DB_HOST", ValueFrom: secretKeyEnv("db-secret", "databaseHost")},
	}

	changed := updateEnvVars(&env, []crmv1.EnvVar{
		{Name: "WORDPRESS_DB_HOST", Value: "override.example.com"},
		{Name: "CUSTOM_SETTING", Value: "enabled"},
	})

	if !changed {
		t.Fatal("updateEnvVars did not report adding custom env var")
	}
	assertSecretRef(t, env, "WORDPRESS_DB_HOST", "db-secret", "databaseHost")
	assertEnvValue(t, env, "CUSTOM_SETTING", "enabled")
}

func assertEnvValue(t *testing.T, env []corev1.EnvVar, name, expected string) {
	t.Helper()
	for _, item := range env {
		if item.Name == name {
			if item.Value != expected {
				t.Fatalf("env %s = %q, want %q", name, item.Value, expected)
			}
			return
		}
	}
	t.Fatalf("env %s not found", name)
}

func assertSecretRef(t *testing.T, env []corev1.EnvVar, name, secretName, key string) {
	t.Helper()
	for _, item := range env {
		if item.Name != name {
			continue
		}
		if item.ValueFrom == nil || item.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("env %s has no secret key ref", name)
		}
		ref := item.ValueFrom.SecretKeyRef
		if ref.Name != secretName || ref.Key != key {
			t.Fatalf("env %s references %s/%s, want %s/%s", name, ref.Name, ref.Key, secretName, key)
		}
		return
	}
	t.Fatalf("env %s not found", name)
}

func TestToCoreEnvVarsSkipsManagedEnvVars(t *testing.T) {
	got := toCoreEnvVars([]crmv1.EnvVar{
		{Name: "WORDPRESS_URL", Value: "https://override.example.com"},
		{Name: "CUSTOM_SETTING", Value: "value"},
	})

	if len(got) != 1 || got[0].Name != "CUSTOM_SETTING" || got[0].Value != "value" {
		t.Fatalf("expected only CUSTOM_SETTING, got %#v", got)
	}
}

func TestGetSFTPServiceTypeDefaultsToLoadBalancer(t *testing.T) {
	for value, want := range map[string]corev1.ServiceType{
		"":             corev1.ServiceTypeLoadBalancer,
		"LoadBalancer": corev1.ServiceTypeLoadBalancer,
		"ClusterIP":    corev1.ServiceTypeClusterIP,
		"NodePort":     corev1.ServiceTypeNodePort,
	} {
		t.Setenv("SFTP_SERVICE_TYPE", value)
		if got := getSFTPServiceType(); got != want {
			t.Fatalf("SFTP_SERVICE_TYPE=%q: expected %s, got %s", value, want, got)
		}
	}
}
