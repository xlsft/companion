package kit

import (
	"slices"
	"strings"

	"gioui.org/layout"

	"github.com/xlsft/companion/internal/ui/i18n"
	"github.com/xlsft/companion/internal/ui/theme"
	"github.com/xlsft/companion/pkg/contracts"
)

// The capability table of the wizard's platform step and of Settings ›
// Diagnostics. It names capabilities in the user's words and explains the
// fallback; backends, desktop names and the probes' technical reasons
// never reach the screen, only the copied diagnostics report.

func init() {
	i18n.Register(map[string][2]string{
		"cap.col.cap":   {"Capability", "Возможность"},
		"cap.col.state": {"Status", "Статус"},
		"cap.col.means": {"What it means", "Что это значит"},

		"cap.status.available":        {"Available", "Есть"},
		"cap.status.degraded":         {"Limited", "Ограничено"},
		"cap.status.needs_permission": {"Needs permission", "Нужно разрешение"},
		"cap.status.unavailable":      {"No", "Нет"},
		"cap.status.unknown":          {"Not checked", "Не проверено"},

		"cap.name.global_hotkey":          {"Hotkey", "Горячая клавиша"},
		"cap.name.hotkey_release":         {"Hold to talk", "Удержание для записи"},
		"cap.name.screen_capture":         {"Screen capture", "Захват экрана"},
		"cap.name.overlay_click_through":  {"Hints over windows", "Подсказки поверх окон"},
		"cap.name.overlay_fallback_panel": {"Hint panel", "Панель подсказок"},
		"cap.name.input_injection":        {"Computer control", "Управление компьютером"},
		"cap.name.accessibility":          {"Accessibility", "Доступность"},
		"cap.name.text_insertion":         {"Text insertion", "Вставка текста"},
		"cap.name.secret_store":           {"Secret storage", "Хранилище секретов"},
		"cap.name.microphone":             {"Microphone", "Микрофон"},
		"cap.name.audio_output":           {"Sound output", "Вывод звука"},
		"cap.name.tray":                   {"Tray icon", "Значок в трее"},

		"cap.ok.global_hotkey":          {"talk from any window", "разговор из любого окна"},
		"cap.ok.hotkey_release":         {"hold the key while you talk", "держите клавишу, пока говорите"},
		"cap.ok.screen_capture":         {"one frame at the moment you ask", "один кадр в момент запроса"},
		"cap.ok.overlay_click_through":  {"clicks pass through", "клики проходят насквозь"},
		"cap.ok.overlay_fallback_panel": {"hints in the main window", "подсказки в главном окне"},
		"cap.ok.input_injection":        {"Companion can press for you on request", "Companion может нажать за вас по просьбе"},
		"cap.ok.accessibility":          {"elements are found by their names", "элементы находятся по названиям"},
		"cap.ok.text_insertion":         {"dictated text goes into fields", "вставка текста в поля"},
		"cap.ok.secret_store":           {"keys never get into the database", "ключи не попадают в базу"},
		"cap.ok.microphone":             {"voice input", "голосовой ввод"},
		"cap.ok.audio_output":           {"answers are spoken", "ответы озвучиваются"},
		"cap.ok.tray":                   {"works without an open window", "работа без открытого окна"},

		"cap.fallback.generic":                {"not available here; Companion works without it", "недоступно; Companion работает без этого"},
		"cap.fallback.global_hotkey":          {"through a shortcut in the system settings", "через сочетание в настройках системы"},
		"cap.fallback.hotkey_release":         {"press to talk, press to send", "нажать — говорить, нажать — отправить"},
		"cap.fallback.screen_capture":         {"a system screen capture component is missing; Companion cannot see the screen", "нет системного компонента захвата экрана; Companion не видит экран"},
		"cap.fallback.overlay_click_through":  {"a panel shows where to look instead of a pointer", "панель подсказывает, куда смотреть, вместо указателя"},
		"cap.fallback.overlay_fallback_panel": {"hints are shown in the main window", "подсказки показываются в главном окне"},
		"cap.fallback.input_injection":        {"Companion shows but does not press itself", "Companion показывает, но не нажимает сам"},
		"cap.fallback.accessibility":          {"elements are found from the screenshot only", "элементы находятся только по снимку экрана"},
		"cap.fallback.text_insertion":         {"dictated text is copied to the clipboard", "текст копируется в буфер обмена"},
		"cap.fallback.secret_store":           {"keys are kept in an encrypted local file", "ключи хранятся в зашифрованном локальном файле"},
		"cap.fallback.microphone":             {"connect a microphone; voice input is required", "подключите микрофон; голосовой ввод обязателен"},
		"cap.fallback.audio_output":           {"answers are shown as text only", "ответы показываются только текстом"},
		"cap.fallback.tray":                   {"closing the last window quits Companion", "закрытие последнего окна завершает Companion"},
		"cap.fallback.unknown":                {"this build cannot check it yet", "эта сборка пока не умеет это проверять"},
	})
}

// capOrder puts what the user meets first at the top; capabilities not
// listed follow alphabetically.
var capOrder = []contracts.Capability{
	contracts.CapMicrophone, contracts.CapScreenCapture, contracts.CapOverlayClickThrough,
	contracts.CapOverlayFallbackPanel, contracts.CapGlobalHotkey, contracts.CapHotkeyRelease,
	contracts.CapInputInjection, contracts.CapAccessibility, contracts.CapTextInsertion,
	contracts.CapAudioOutput, contracts.CapSecretStore, contracts.CapTray,
}

// OrderedCapabilities returns the report's capabilities in display order.
func OrderedCapabilities(r contracts.CapabilityReport) []contracts.Capability {
	caps := make([]contracts.Capability, 0, len(r.Capabilities))
	for c := range r.Capabilities {
		caps = append(caps, c)
	}
	slices.Sort(caps)
	rank := func(c contracts.Capability) int {
		if i := slices.Index(capOrder, c); i >= 0 {
			return i
		}
		return len(capOrder)
	}
	slices.SortStableFunc(caps, func(a, b contracts.Capability) int { return rank(a) - rank(b) })
	return caps
}

// CapName is the user-facing name of a capability.
func CapName(l i18n.Lang, c contracts.Capability) string {
	if k := "cap.name." + string(c); i18n.Has(k) {
		return i18n.T(l, k)
	}
	s := strings.ReplaceAll(string(c), "_", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// CapNote explains what a capability's state means for the user and,
// when it is missing or limited, the fallback (PRD §10).
func CapNote(l i18n.Lang, c contracts.Capability, st contracts.CapabilityState) string {
	switch st.Status {
	case contracts.StatusAvailable:
		if k := "cap.ok." + string(c); i18n.Has(k) {
			return i18n.T(l, k)
		}
		return ""
	case contracts.StatusUnknown, "":
		return i18n.T(l, "cap.fallback.unknown")
	}
	if k := "cap.fallback." + string(c); i18n.Has(k) {
		return i18n.T(l, k)
	}
	return i18n.T(l, "cap.fallback.generic")
}

// CapStatus maps a capability status to its display status and word.
func CapStatus(l i18n.Lang, s contracts.Status) (theme.Status, string) {
	switch s {
	case contracts.StatusAvailable:
		return theme.StatusOK, i18n.T(l, "cap.status.available")
	case contracts.StatusDegraded:
		return theme.StatusWarn, i18n.T(l, "cap.status.degraded")
	case contracts.StatusNeedsPermission:
		return theme.StatusWarn, i18n.T(l, "cap.status.needs_permission")
	case contracts.StatusUnavailable:
		return theme.StatusError, i18n.T(l, "cap.status.unavailable")
	}
	return theme.StatusUnknown, i18n.T(l, "cap.status.unknown")
}

// CapabilityTexts are every text CapabilityTable shows for r, in order;
// tests check them for environment names.
func CapabilityTexts(l i18n.Lang, r contracts.CapabilityReport) []string {
	out := []string{i18n.T(l, "cap.col.cap"), i18n.T(l, "cap.col.state"), i18n.T(l, "cap.col.means")}
	for _, c := range OrderedCapabilities(r) {
		st := r.Capabilities[c]
		_, word := CapStatus(l, st.Status)
		out = append(out, CapName(l, c), word, CapNote(l, c, st))
	}
	return out
}

// CapabilityTable is the report as a Panel table: capability, status
// badge and what it means. rec, if set, receives every shown text.
func CapabilityTable(th *theme.Theme, l i18n.Lang, r contracts.CapabilityReport, rec func(string)) layout.Widget {
	if rec != nil {
		for _, s := range CapabilityTexts(l, r) {
			rec(s)
		}
	}
	// Column widths of the prototype (260 / 170 / rest), shrunk with the
	// window.
	cols := func(gtx layout.Context, a, b, c layout.Widget) layout.Dimensions {
		full := gtx.Constraints.Max.X - gtx.Dp(th.Dp(40))
		w1 := min(gtx.Dp(th.Dp(240)), full*30/100)
		w2 := min(gtx.Dp(th.Dp(180)), full*26/100)
		fixed := func(px int, wd layout.Widget) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.X, gtx.Constraints.Max.X = px, px
				d := wd(gtx)
				d.Size.X = px // a badge hugs its word; the column keeps its width
				return d
			})
		}
		return layout.Inset{Left: th.Dp(20), Right: th.Dp(20)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx, fixed(w1, a), fixed(w2, b), layout.Flexed(1, c))
		})
	}
	head := func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: th.Dp(10), Bottom: th.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return cols(gtx, Caption(th, i18n.T(l, "cap.col.cap")).Layout, Caption(th, i18n.T(l, "cap.col.state")).Layout,
				Caption(th, i18n.T(l, "cap.col.means")).Layout)
		})
	}
	rows := []layout.Widget{head}
	// Narrow (small window or large text): each capability is a block of
	// name and badge over the explanation, without the column header.
	var stacked []layout.Widget
	for _, c := range OrderedCapabilities(r) {
		st := r.Capabilities[c]
		ds, word := CapStatus(l, st.Status)
		name := Medium(label(th, 14, CapName(l, c), th.Pal.Fg)).Layout
		badge := func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: th.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return Badge(gtx, th, ds, word)
			})
		}
		note := label(th, 13, CapNote(l, c, st), th.Pal.Muted).Layout
		rows = append(rows, func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Top: th.Dp(8), Bottom: th.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return VCenter(gtx, gtx.Dp(th.Dp(30)), func(gtx layout.Context) layout.Dimensions {
					return cols(gtx, name, badge, note)
				})
			})
		})
		stacked = append(stacked, Pad(th, 12, 20, VStack(th, 6, Spread(name, func(gtx layout.Context) layout.Dimensions {
			return Badge(gtx, th, ds, word)
		}), note)))
	}
	return func(gtx layout.Context) layout.Dimensions {
		if gtx.Constraints.Max.X < gtx.Dp(th.Dp(600)) {
			return Rows(gtx, th, stacked...)
		}
		return Rows(gtx, th, rows...)
	}
}
