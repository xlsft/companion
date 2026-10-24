package bench

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Report is the JSON document written by cmd/stt-bench.
type Report struct {
	Date    string         `json:"date"`
	Machine map[string]any `json:"machine"`
	Corpus  map[string]any `json:"corpus"`
	Method  map[string]any `json:"method"`
	Results []Result       `json:"results"`
}

func na(v float64, f string) string {
	if v <= 0 {
		return "n/a"
	}
	return fmt.Sprintf(f, v)
}

// WriteMarkdown renders one table per language, models in the order run.
func WriteMarkdown(w io.Writer, rs []Result) {
	by := map[string][]Result{}
	var langs []string
	for _, r := range rs {
		if _, ok := by[r.Lang]; !ok {
			langs = append(langs, r.Lang)
		}
		by[r.Lang] = append(by[r.Lang], r)
	}
	sort.Strings(langs)
	for _, l := range langs {
		fmt.Fprintf(w, "### %s\n\n", l)
		fmt.Fprintln(w, "| Model | Backend | Load ms | RTF mean | RTF p95 | Final decode p50 ms | EOS→final p50 ms | EOS→final max ms | First partial p50 ms | Peak RSS MB | WER % | CER % | VAD misses |")
		fmt.Fprintln(w, "|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|")
		for _, r := range by[l] {
			name := strings.TrimSuffix(strings.TrimPrefix(r.Model, "ggml-"), ".bin")
			if r.Error != "" {
				fmt.Fprintf(w, "| %s | %s | failed: %s ||||||||||| \n", name, r.BackendAsked, strings.ReplaceAll(r.Error, "|", "/"))
				continue
			}
			fmt.Fprintf(w, "| %s | %s | %.0f | %.3f | %.3f | %.0f | %.0f | %.0f | %s | %s | %.1f | %.1f | %d |\n",
				name, r.BackendInUse, r.LoadMS, r.RTFMean, r.RTFP95, r.FinalBatchP50, r.EOSFinalP50, r.EOSFinalMax,
				na(r.FirstPartialP50, "%.0f"), na(r.PeakRSSMB, "%.0f"), r.WER*100, r.CER*100, r.NoSpeech)
		}
		fmt.Fprintln(w)
	}
}
