package wordpress

import (
	crmv1 "hostzero.de/m/v2/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"os"
)

// GetIndependentCommonLabels returns common labels that are independent of e.g. the application version, of new custom labels on the CRD, ...
// this is useful for searching resources that belong to a specific WordPressSite, they may have been created before such labels were added
func GetIndependentCommonLabels(wp *crmv1.WordPressSite) map[string]string {
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "kubepress-operator",
		"app.kubernetes.io/part-of":    "kubepress",
	}

	// add an instance label for easier filtering
	labels["app.kubernetes.io/instance"] = wp.Name

	// Add the UID as a label for labelSelector usage
	labels["hostzero.com/resource-uid"] = string(wp.GetUID())

	return labels
}

// GetCommonLabels returns common labels for all WordPress resources
func GetCommonLabels(wp *crmv1.WordPressSite, extraLabels ...map[string]string) map[string]string {
	labels := GetIndependentCommonLabels(wp)

	// Kubernetes recommended labels to exclude from copying
	recommendedLabels := map[string]struct{}{
		"app.kubernetes.io/name":       {},
		"app.kubernetes.io/instance":   {},
		"app.kubernetes.io/version":    {},
		"app.kubernetes.io/component":  {},
		"app.kubernetes.io/part-of":    {},
		"app.kubernetes.io/managed-by": {},
	}

	// copy any non-standard labels from the WordPress site to the resource
	for key, value := range wp.ObjectMeta.Labels {
		if _, isRecommended := recommendedLabels[key]; !isRecommended {
			labels[key] = value
		}
	}

	// add version label
	version := os.Getenv("VERSION")
	if version != "" {
		labels["app.kubernetes.io/version"] = version
	}

	// Add any extra labels
	for _, extra := range extraLabels {
		for k, v := range extra {
			labels[k] = v
		}
	}

	return labels
}

func GetWordpressLabels(wp *crmv1.WordPressSite, extraLabels ...map[string]string) map[string]string {
	labels := GetCommonLabels(wp, extraLabels...)
	labels["app.kubernetes.io/component"] = "wordpress"
	return labels
}

func GetWordpressLabelsForMatching(wp *crmv1.WordPressSite, extraLabels ...map[string]string) map[string]string {
	labels := GetIndependentCommonLabels(wp)
	labels["app.kubernetes.io/component"] = "wordpress"

	// add any extra labels
	for _, extra := range extraLabels {
		for k, v := range extra {
			labels[k] = v
		}
	}

	return labels
}

func GetDatabaseLabels(wp *crmv1.WordPressSite, extraLabels ...map[string]string) map[string]string {
	labels := GetCommonLabels(wp, extraLabels...)
	labels["app.kubernetes.io/component"] = "database"
	return labels
}

func GetSFTPLabels(wp *crmv1.WordPressSite, extraLabels ...map[string]string) map[string]string {
	labels := GetCommonLabels(wp, extraLabels...)
	labels["app.kubernetes.io/component"] = "sftp"
	return labels
}

func GetSFTPLabelsForMatching(wp *crmv1.WordPressSite, extraLabels ...map[string]string) map[string]string {
	labels := GetIndependentCommonLabels(wp)
	labels["app.kubernetes.io/component"] = "sftp"

	// add any extra labels
	for _, extra := range extraLabels {
		for k, v := range extra {
			labels[k] = v
		}
	}

	return labels
}

func GetPHPMyAdminLabels(wp *crmv1.WordPressSite, extraLabels ...map[string]string) map[string]string {
	labels := GetCommonLabels(wp, extraLabels...)
	labels["app.kubernetes.io/component"] = "phpmyadmin"
	return labels
}

// SetCondition sets or updates a status condition
func SetCondition(wp *crmv1.WordPressSite, conditionType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		ObservedGeneration: wp.Generation,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	}

	// Find and update existing condition or append new one
	for i, c := range wp.Status.Conditions {
		if c.Type == conditionType {
			// Only update if something changed to avoid unnecessary updates
			if c.Status != status || c.Reason != reason || c.Message != message {
				wp.Status.Conditions[i] = condition
			}
			return
		}
	}

	// If Condition doesn't exist, add it
	wp.Status.Conditions = append(wp.Status.Conditions, condition)
}
