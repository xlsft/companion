package providers

import "github.com/xlsft/companion/internal/llm/probe"

// Detail key suffixes and prefixes of TestResult.Detail.
const (
	DetailMessage   = ".msg"
	DetailCode      = ".code"
	DetailCapPrefix = "cap."
	DetailProvider  = "provider_id"
)

// Detail flattens the result into the setup wizard's readiness detail map
// (OC-70): "<step>" = Status, "<step>.msg" = MessageKey, "<step>.code" =
// Code, "cap.<capability>" = ok|failed|skipped for text, stream, vision and
// tools, and "provider_id". Only technical codes, so it may be persisted.
func (r TestResult) Detail() map[string]string {
	d := map[string]string{DetailProvider: r.ProviderID}
	for _, s := range r.Steps {
		d[s.ID] = string(s.Status)
		if s.MessageKey != "" {
			d[s.ID+DetailMessage] = s.MessageKey
		}
		if s.Code != "" {
			d[s.ID+DetailCode] = s.Code
		}
	}
	for _, c := range probe.DefaultWant {
		v := string(StatusSkipped)
		if r.Capabilities != nil {
			if res, ok := r.Capabilities.Get(c); ok && !res.Skipped {
				v = string(StatusFailed)
				if res.Effective() {
					v = string(StatusOK)
				}
			}
		}
		d[DetailCapPrefix+string(c)] = v
	}
	return d
}
