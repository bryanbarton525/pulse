package controller

import (
	"testing"
	"time"
)

func TestStatusSyncerProbeRunnerResultsURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		syncer   StatusSyncer
		expected string
	}{
		{
			name: "uses explicit override when configured",
			syncer: StatusSyncer{
				Namespace:  "pulse-system",
				Interval:   15 * time.Second,
				ResultsURL: "http://127.0.0.1:9090/results",
			},
			expected: "http://127.0.0.1:9090/results",
		},
		{
			name: "falls back to in cluster service url",
			syncer: StatusSyncer{
				Namespace: "pulse-system",
				Interval:  15 * time.Second,
			},
			expected: "http://pulse-probe-runner.pulse-system.svc:9090/results",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.syncer.probeRunnerResultsURL(); got != tt.expected {
				t.Fatalf("probeRunnerResultsURL() = %q, want %q", got, tt.expected)
			}
		})
	}
}
