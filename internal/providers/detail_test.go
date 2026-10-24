package providers

import (
	"maps"
	"testing"

	"github.com/xlsft/companion/internal/llm"
	"github.com/xlsft/companion/internal/llm/probe"
)

func TestResultDetail(t *testing.T) {
	yes := true
	tests := []struct {
		name string
		in   TestResult
		want map[string]string
	}{
		{
			name: "auth failed",
			in: TestResult{ProviderID: "p1", Steps: []StepResult{
				{ID: StepConnection, Status: StatusOK, MessageKey: "provider.connection.ok"},
				{ID: StepAuthModel, Status: StatusFailed, MessageKey: "provider.error.auth", Code: "auth"},
				{ID: StepCapabilities, Status: StatusSkipped, MessageKey: "provider.skipped"},
			}},
			want: map[string]string{
				"provider_id": "p1",
				"connection":  "ok", "connection.msg": "provider.connection.ok",
				"auth_model": "failed", "auth_model.msg": "provider.error.auth", "auth_model.code": "auth",
				"capabilities": "skipped", "capabilities.msg": "provider.skipped",
				"cap.text": "skipped", "cap.stream": "skipped", "cap.vision": "skipped", "cap.tools": "skipped",
			},
		},
		{
			name: "capabilities with override",
			in: TestResult{ProviderID: "p2", Steps: []StepResult{
				{ID: StepCapabilities, Status: StatusOK, MessageKey: "provider.capabilities.ok"},
			}, Capabilities: &probe.Report{Results: []probe.Result{
				{Capability: probe.CapText, OK: true},
				{Capability: probe.CapStream, OK: false, ErrCode: "no_stream"},
				{Capability: llm.CapVision, OK: false, Override: &yes},
				{Capability: llm.CapTools, Skipped: true},
			}}},
			want: map[string]string{
				"provider_id":  "p2",
				"capabilities": "ok", "capabilities.msg": "provider.capabilities.ok",
				"cap.text": "ok", "cap.stream": "failed", "cap.vision": "ok", "cap.tools": "skipped",
			},
		},
	}
	for _, tt := range tests {
		if got := tt.in.Detail(); !maps.Equal(got, tt.want) {
			t.Errorf("%s:\n got %v\nwant %v", tt.name, got, tt.want)
		}
	}
}
