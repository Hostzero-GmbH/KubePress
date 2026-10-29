package wordpress

import "testing"

func TestComputeApachePreforkSettings(t *testing.T) {
	tests := []struct {
		name   string
		memory string
		cpu    string
		want   ApachePreforkSettings
	}{
		// corepanel tiers
		{"tier1 0.5cpu/1Gi", "1Gi", "500m", ApachePreforkSettings{MaxRequestWorkers: 8, StartServers: 4, MinSpareServers: 4, MaxSpareServers: 6}},
		{"tier2 1.5cpu/2Gi", "2Gi", "1500m", ApachePreforkSettings{MaxRequestWorkers: 24, StartServers: 12, MinSpareServers: 12, MaxSpareServers: 18}},
		{"tier3 3cpu/4Gi", "4Gi", "3000m", ApachePreforkSettings{MaxRequestWorkers: 48, StartServers: 24, MinSpareServers: 24, MaxSpareServers: 36}},
		// memory-bound: plenty of CPU, little memory
		{"memory bound", "1Gi", "4", ApachePreforkSettings{MaxRequestWorkers: 15, StartServers: 7, MinSpareServers: 7, MaxSpareServers: 11}},
		// floor: tiny container still gets a usable pool
		{"floor", "256Mi", "100m", ApachePreforkSettings{MaxRequestWorkers: 4, StartServers: 2, MinSpareServers: 2, MaxSpareServers: 3}},
		// ceiling
		{"ceiling", "64Gi", "32", ApachePreforkSettings{MaxRequestWorkers: 150, StartServers: 75, MinSpareServers: 75, MaxSpareServers: 112}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ComputeApachePreforkSettings(tt.memory, tt.cpu)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}

	if _, err := ComputeApachePreforkSettings("lots", "1"); err == nil {
		t.Error("expected error for invalid memory limit")
	}
}
