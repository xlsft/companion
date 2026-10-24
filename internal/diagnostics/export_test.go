package diagnostics

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/xlsft/companion/internal/diagnostics/logging"
	"github.com/xlsft/companion/pkg/contracts"
)

func sampleReport() Report {
	return Report{
		GeneratedAt: fixedNow,
		AppVersion:  "1.2.3",
		Commit:      "abc1234",
		Capabilities: contracts.CapabilityReport{
			OS: "linux", Session: contracts.SessionWayland,
			Capabilities: map[contracts.Capability]contracts.CapabilityState{
				contracts.CapScreenCapture: {Status: contracts.StatusAvailable, Backend: "xdg-portal"},
			},
		},
		Providers:    []ProviderSummary{{ID: "p1", Kind: "llm", BaseHost: "api.example.com", Model: "m"}},
		STT:          &STTStatus{ModelID: "base", Backend: "cpu", State: "ready"},
		RecentErrors: []logging.ErrorEvent{{Time: fixedNow, Level: "ERROR", Component: "stt", Code: "CRASH", Message: "died"}},
	}
}

func readZip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(data)
	}
	return out
}

func TestExportContentsAndPreview(t *testing.T) {
	r := sampleReport()
	var buf bytes.Buffer
	if err := Export(r, &buf); err != nil {
		t.Fatal(err)
	}
	files := readZip(t, buf.Bytes())

	prev := Preview(r)
	wantNames := []string{"report.json", "capabilities.json", "errors.jsonl", "manifest.json"}
	if len(prev) != len(wantNames) || len(files) != len(wantNames) {
		t.Fatalf("preview %d files %d", len(prev), len(files))
	}
	for i, e := range prev {
		if e.Name != wantNames[i] {
			t.Errorf("order %d: %s", i, e.Name)
		}
		if files[e.Name] != e.Text || e.Size != len(e.Text) {
			t.Errorf("%s: preview differs from archive", e.Name)
		}
	}

	var rep Report
	if err := json.Unmarshal([]byte(files["report.json"]), &rep); err != nil || rep.AppVersion != "1.2.3" {
		t.Errorf("report.json: %v %+v", err, rep)
	}
	var caps contracts.CapabilityReport
	if err := json.Unmarshal([]byte(files["capabilities.json"]), &caps); err != nil || caps.OS != "linux" {
		t.Errorf("capabilities.json: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(files["errors.jsonl"]), "\n")
	var ev logging.ErrorEvent
	if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &ev) != nil || ev.Code != "CRASH" {
		t.Errorf("errors.jsonl: %q", files["errors.jsonl"])
	}
	var m manifest
	if err := json.Unmarshal([]byte(files["manifest.json"]), &m); err != nil {
		t.Fatal(err)
	}
	if !m.GeneratedAt.Equal(fixedNow) || len(m.Files) != 3 {
		t.Fatalf("manifest %+v", m)
	}
	for _, f := range m.Files {
		if f.Size != len(files[f.Name]) {
			t.Errorf("manifest size for %s: %d vs %d", f.Name, f.Size, len(files[f.Name]))
		}
	}
}

func TestExportEmptyErrors(t *testing.T) {
	r := sampleReport()
	r.RecentErrors = nil
	var buf bytes.Buffer
	if err := Export(r, &buf); err != nil {
		t.Fatal(err)
	}
	if got := readZip(t, buf.Bytes())["errors.jsonl"]; got != "" {
		t.Fatalf("%q", got)
	}
}

func TestExportRejectsPlantedSecrets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(r *Report)
		file   string
	}{
		{"provider model", func(r *Report) { r.Providers[0].Model = "sk-ant-api03-AbCdEf123456" }, "report.json"},
		{"stt reason", func(r *Report) { r.STT.Reason = "Bearer abcdefgh12345678" }, "report.json"},
		{"capability fact", func(r *Report) {
			r.Capabilities.Facts = map[string]string{"x": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"}
		}, "report.json"},
		{"error message", func(r *Report) { r.RecentErrors[0].Message = "key xi-abcdefghijkl1234" }, "report.json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := sampleReport()
			tc.mutate(&r)
			var buf bytes.Buffer
			err := Export(r, &buf)
			if !errors.Is(err, ErrSecretDetected) {
				t.Fatalf("err=%v", err)
			}
			if buf.Len() != 0 {
				t.Error("partial archive written")
			}
			if strings.Contains(err.Error(), "AbCdEf") || strings.Contains(err.Error(), "abcdefgh") {
				t.Errorf("error leaks secret: %v", err)
			}
		})
	}
}

func TestSampleReportIsClean(t *testing.T) {
	prev := Preview(sampleReport())
	for _, e := range prev {
		if logging.ContainsSecret(e.Text) {
			t.Errorf("%s flagged: false positive", e.Name)
		}
	}
}
