package kit

import (
	"slices"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/pkg/contracts"
)

func init() {
	i18n.Register(map[string][2]string{
		"status.available":        {"available", "доступно"},
		"status.degraded":         {"limited", "ограничено"},
		"status.needs_permission": {"needs permission", "нужно разрешение"},
		"status.unavailable":      {"unavailable", "недоступно"},
		"status.unknown":          {"not checked", "не проверено"},
	})
}

// StatusOf maps a capability status to its display status and label key.
func StatusOf(s contracts.Status) (theme.Status, string) {
	switch s {
	case contracts.StatusAvailable:
		return theme.StatusOK, "status.available"
	case contracts.StatusDegraded:
		return theme.StatusWarn, "status.degraded"
	case contracts.StatusNeedsPermission:
		return theme.StatusWarn, "status.needs_permission"
	case contracts.StatusUnavailable:
		return theme.StatusError, "status.unavailable"
	}
	return theme.StatusUnknown, "status.unknown"
}

// SortedCapabilities returns the report's capabilities in stable order.
func SortedCapabilities(r contracts.CapabilityReport) []contracts.Capability {
	caps := make([]contracts.Capability, 0, len(r.Capabilities))
	for c := range r.Capabilities {
		caps = append(caps, c)
	}
	slices.Sort(caps)
	return caps
}

// CapabilityRows renders one row per capability: status symbol and word,
// backend, and the technical reason underneath. extra, if set, adds a line
// per capability (the wizard's fallback explanation).
func CapabilityRows(th *theme.Theme, l i18n.Lang, r contracts.CapabilityReport, extra func(contracts.Capability, contracts.CapabilityState) string) []layout.Widget {
	var rows []layout.Widget
	for _, c := range SortedCapabilities(r) {
		st := r.Capabilities[c]
		ds, key := StatusOf(st.Status)
		txt := string(c) + " — " + i18n.T(l, key)
		if st.Backend != "" {
			txt += " (" + st.Backend + ")"
		}
		var note string
		if extra != nil {
			note = extra(c, st)
		}
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: 6}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(th.StatusLabel(ds, txt).Layout),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if st.Reason == "" {
							return layout.Dimensions{}
						}
						return layout.Inset{Left: 20}.Layout(gtx, Muted(th, st.Reason).Layout)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						if note == "" {
							return layout.Dimensions{}
						}
						return layout.Inset{Left: 20}.Layout(gtx, Muted(th, note).Layout)
					}),
				)
			})
		})
	}
	return rows
}
