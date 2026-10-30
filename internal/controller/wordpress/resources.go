package wordpress

import (
	"fmt"

	crmv1 "hostzero.de/m/v2/api/v1"
	"hostzero.de/m/v2/internal/config"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// WordPressResources returns the resource requirements of the WordPress
// container. Limits come from the site spec; requests are derived from the
// limits with the operator-wide fractions (REQUEST_CPU_FRACTION,
// REQUEST_MEMORY_FRACTION), so the cluster can overcommit on requests
// while every site keeps its full limit. The cpuRequest/memoryRequest spec
// fields are ignored.
func WordPressResources(wp *crmv1.WordPressSite) (corev1.ResourceRequirements, error) {
	cpuLimit, err := resource.ParseQuantity(wp.Spec.WordPress.Resources.CPULimit)
	if err != nil {
		return corev1.ResourceRequirements{}, fmt.Errorf("invalid cpuLimit %q: %w", wp.Spec.WordPress.Resources.CPULimit, err)
	}
	memLimit, err := resource.ParseQuantity(wp.Spec.WordPress.Resources.MemoryLimit)
	if err != nil {
		return corev1.ResourceRequirements{}, fmt.Errorf("invalid memoryLimit %q: %w", wp.Spec.WordPress.Resources.MemoryLimit, err)
	}

	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    scaleQuantity(cpuLimit, config.AppConfig.RequestCPUFraction),
			corev1.ResourceMemory: scaleQuantity(memLimit, config.AppConfig.RequestMemoryFraction),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    cpuLimit,
			corev1.ResourceMemory: memLimit,
		},
	}, nil
}

// scaleQuantity multiplies q by fraction, keeping milli precision for CPU
// and whole MiB for memory so the result stays readable in the Deployment.
func scaleQuantity(q resource.Quantity, fraction float64) resource.Quantity {
	if q.Format == resource.BinarySI {
		mib := float64(q.Value()) / (1024 * 1024) * fraction
		if mib < 1 {
			mib = 1
		}
		return resource.MustParse(fmt.Sprintf("%dMi", int64(mib)))
	}
	milli := int64(float64(q.MilliValue()) * fraction)
	if milli < 1 {
		milli = 1
	}
	return *resource.NewMilliQuantity(milli, resource.DecimalSI)
}
