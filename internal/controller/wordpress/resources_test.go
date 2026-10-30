package wordpress

import (
	"testing"

	crmv1 "hostzero.de/m/v2/api/v1"
	"hostzero.de/m/v2/internal/config"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestWordPressResources(t *testing.T) {
	config.AppConfig.RequestCPUFraction = 0.25
	config.AppConfig.RequestMemoryFraction = 0.5

	tests := []struct {
		cpuLimit, memLimit string
		wantCPU, wantMem   string
	}{
		{"500m", "1Gi", "125m", "512Mi"},
		{"1500m", "2Gi", "375m", "1024Mi"},
		{"3", "4Gi", "750m", "2048Mi"},
		{"1m", "1Mi", "1m", "1Mi"},
	}
	for _, tt := range tests {
		wp := &crmv1.WordPressSite{}
		wp.Spec.WordPress.Resources = &crmv1.ResourceRequirements{CPULimit: tt.cpuLimit, MemoryLimit: tt.memLimit, CPURequest: "999", MemoryRequest: "999Gi"}
		got, err := WordPressResources(wp)
		if err != nil {
			t.Fatalf("%s/%s: %v", tt.cpuLimit, tt.memLimit, err)
		}
		if c := got.Requests.Cpu(); c.Cmp(resource.MustParse(tt.wantCPU)) != 0 {
			t.Errorf("%s/%s: cpu request %s, want %s", tt.cpuLimit, tt.memLimit, c.String(), tt.wantCPU)
		}
		if m := got.Requests.Memory(); m.Cmp(resource.MustParse(tt.wantMem)) != 0 {
			t.Errorf("%s/%s: memory request %s, want %s", tt.cpuLimit, tt.memLimit, m.String(), tt.wantMem)
		}
		if got.Limits.Cpu().Cmp(*got.Requests.Cpu()) < 0 || got.Limits.Memory().Cmp(*got.Requests.Memory()) < 0 {
			t.Errorf("%s/%s: request exceeds limit", tt.cpuLimit, tt.memLimit)
		}
	}
}
