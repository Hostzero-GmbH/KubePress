package wordpress

import (
	"context"
	"fmt"
	"github.com/go-logr/logr"
	crmv1 "hostzero.de/m/v2/api/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"math/rand"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"time"
)

const SSHKeysVolumeName = "ssh-keys-config"
const SFTPPortMin = 10000
const SFTPPortMax = 32767

func ReconcileSFTPDeployment(ctx context.Context, r client.Client, scheme *runtime.Scheme, wp *crmv1.WordPressSite, username string) error {
	logger := log.FromContext(ctx).WithValues("component", "sftp-deployment")

	logger.Info("Username correctly set to", "username", username)
	deploymentName := wp.Name + "-sftp"

	deployment := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: wp.Namespace}, deployment)

	if errors.IsNotFound(err) {
		mySQLSecretName := wp.Spec.AdminUserSecretKeyRef

		// Ensure SSH host key Secret exists in this namespace
		sshSecretName := "sftp-ssh-host-keys"
		sshSecret := &corev1.Secret{}
		err = r.Get(ctx, types.NamespacedName{Name: sshSecretName, Namespace: wp.Namespace}, sshSecret)
		if errors.IsNotFound(err) {
			// Generate keys
			rsaKey, ed25519Key, genErr := GenerateSSHHostKeys()
			if genErr != nil {
				logger.Error(genErr, "Failed to generate SSH host keys")
				return genErr
			}
			labels := map[string]string{}
			labels["app.kubernetes.io/managed-by"] = "kubepress-operator"
			sshSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      sshSecretName,
					Namespace: wp.Namespace,
					Labels:    labels,
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"ssh_host_rsa_key":     rsaKey,
					"ssh_host_ed25519_key": ed25519Key,
				},
			}

			// Set owner reference
			if err := controllerutil.SetControllerReference(wp, sshSecret, scheme); err != nil {
				logger.Error(err, "Unable to set owner reference to SSH Secret", "object", sshSecret.GetName())
				return err
			}

			if err := r.Create(ctx, sshSecret); err != nil {
				logger.Error(err, "Failed to create SSH host key Secret")
				return err
			}
			logger.Info("Created SSH host key Secret", "name", sshSecretName)
		} else if err != nil {
			logger.Error(err, "Failed to get SSH host key Secret")
			return fmt.Errorf("failed to get SSH host key Secret %s: %w", sshSecretName, err)
		}

		accessModeInt := int32(0400)

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
				Name: SSHKeysVolumeName,
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: sshSecretName,
						Items: []corev1.KeyToPath{
							{Key: "ssh_host_rsa_key", Path: "ssh_host_rsa_key", Mode: &accessModeInt},
							{Key: "ssh_host_ed25519_key", Path: "ssh_host_ed25519_key", Mode: &accessModeInt},
						},
					},
				},
			},
		}

		volumeMounts := []corev1.VolumeMount{
			{
				Name:      DefaultVolumeName,
				MountPath: "/home/" + username + "/wordpress",
			},
			{
				Name:      SSHKeysVolumeName,
				MountPath: "/etc/ssh/ssh_host_rsa_key",
				SubPath:   "ssh_host_rsa_key",
			},
			{
				Name:      SSHKeysVolumeName,
				MountPath: "/etc/ssh/ssh_host_ed25519_key",
				SubPath:   "ssh_host_ed25519_key",
			},
		}

		env := []corev1.EnvVar{
			{
				Name: "USERNAME",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: mySQLSecretName},
						Key:                  "username",
					},
				},
			},
			{
				Name: "PASSWORD",
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: mySQLSecretName},
						Key:                  "password",
					},
				},
			},
			{
				Name:  "SFTP_USERS",
				Value: "$(USERNAME):$(PASSWORD):33:33",
			},
		}

		replicas := int32(1)

		deploymentLabels := GetSFTPLabels(wp, map[string]string{
			"app.kubernetes.io/name": "sftp-server",
		})
		deploymentLabelsForMatching := GetSFTPLabelsForMatching(wp, map[string]string{
			"app.kubernetes.io/managed-by": "kubepress-operator",
		})

		deployment = &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deploymentName,
				Namespace: wp.Namespace,
				Labels:    deploymentLabels,
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: deploymentLabelsForMatching,
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: deploymentLabels,
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:         "sftp",
								Image:        "atmoz/sftp:latest",
								Env:          env,
								VolumeMounts: volumeMounts,
								Ports: []corev1.ContainerPort{
									{
										Name:          "sftp",
										ContainerPort: 22,
									},
								},
							},
						},
						Volumes: volumes,
					},
				},
			},
		}

		// Set owner reference
		if err := controllerutil.SetControllerReference(wp, deployment, scheme); err != nil {
			logger.Error(err, "Unable to set owner reference to SFTP deployment", "object", deployment.GetName())
			return err
		}

		if err := r.Create(ctx, deployment); err != nil {
			logger.Error(err, "Failed to create SFTP deployment")
			return fmt.Errorf("failed to create SFTP deployment %s: %w", deploymentName, err)
		}

	} else if err != nil {
		logger.Error(err, "Failed to get SFTP deployment")
		return fmt.Errorf("failed to get SFTP deployment %s: %w", deploymentName, err)
	}

	// Ensure SFTP Service exists (LoadBalancer)
	serviceName := GetSFTPServiceName(wp.Name)
	service := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: serviceName, Namespace: wp.Namespace}, service)
	if errors.IsNotFound(err) {
		sftpServiceLabels := GetSFTPLabels(wp, map[string]string{
			"app.kubernetes.io/name": "sftp-service",
		})

		annotations := map[string]string{}
		port := 22
		if sharingKey := os.Getenv("CILIUM_SHARING_KEY"); sharingKey != "" {
			annotations["lbipam.cilium.io/sharing-key"] = sharingKey

			// Request IP
			requestedIPs := os.Getenv("CILIUM_REQUESTED_IPS")

			if requestedIPs != "" {
				annotations["lbipam.cilium.io/ips"] = requestedIPs
			}

			// Allocate a port for this service
			port, err = getAvailablePort(ctx, r, logger)
			if err != nil {
				logger.Error(err, "Failed to allocate SFTP port")
				return fmt.Errorf("failed to allocate SFTP port: %w", err)
			}
		}

		service = &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:        serviceName,
				Namespace:   wp.Namespace,
				Labels:      sftpServiceLabels,
				Annotations: annotations,
			},
			Spec: corev1.ServiceSpec{
				Type:     corev1.ServiceTypeLoadBalancer,
				Selector: GetSFTPLabels(wp),
				Ports: []corev1.ServicePort{{
					Name:       "sftp",
					Port:       int32(port),
					TargetPort: intstr.FromInt(22),
				}},
			},
		}

		// Set owner reference
		if err := controllerutil.SetControllerReference(wp, service, scheme); err != nil {
			logger.Error(err, "Unable to set owner reference to SFTP Service", "object", service.GetName())
			return err
		}

		if err := r.Create(ctx, service); err != nil {
			logger.Error(err, "Failed to create SFTP service")
			return fmt.Errorf("failed to create SFTP service %s: %w", serviceName, err)
		}
		logger.Info("Successfully created SFTP LoadBalancer service", "name", serviceName)
	} else if err != nil {
		logger.Error(err, "Failed to get SFTP service")
		return fmt.Errorf("failed to get SFTP service %s: %w", serviceName, err)
	}

	return nil
}

func getAvailablePort(ctx context.Context, r client.Client, logger logr.Logger) (int, error) {
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "kubepress-operator",
		"app.kubernetes.io/part-of":    "kubepress",
		"app.kubernetes.io/component":  "sftp",
		"app.kubernetes.io/name":       "sftp-service",
	}
	selector := client.MatchingLabels(labels)
	var svcList corev1.ServiceList
	if err := r.List(ctx, &svcList, selector); err != nil {
		return 0, fmt.Errorf("failed to list SFTP services: %w", err)
	}

	usedPorts := map[int]bool{}
	for _, svc := range svcList.Items {
		for _, port := range svc.Spec.Ports {
			usedPorts[int(port.Port)] = true
		}
	}

	rand.Seed(time.Now().UnixNano())
	for i := 0; i < 1000; i++ {
		candidate := rand.Intn(SFTPPortMax-SFTPPortMin+1) + SFTPPortMin
		if _, exists := usedPorts[candidate]; !exists {
			return candidate, nil
		}
	}

	logger.Error(fmt.Errorf("no available SFTP port found in range"), "range", fmt.Sprintf("%d-%d", SFTPPortMin, SFTPPortMax), "usedPorts", usedPorts)
	return 0, fmt.Errorf("no available SFTP port found in range")
}
