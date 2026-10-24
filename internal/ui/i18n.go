package ui

import "github.com/xlsft/companion/internal/ui/i18n"

// Lang is a UI language; values match settings.UI.Language.
type Lang = i18n.Lang

const (
	LangRU = i18n.RU
	LangEN = i18n.EN
)

func init() { i18n.Register(catalog) }

// catalog maps a key to its {en, ru} text. A real catalogue arrives with
// localisation work; the shell only needs its own labels.
var catalog = map[string][2]string{
	"app.name": {"Companion", "Companion"},

	"nav.home":        {"Home", "Главная"},
	"nav.history":     {"History", "История"},
	"nav.agents":      {"Agents", "Агенты"},
	"nav.skills":      {"Skills", "Навыки"},
	"nav.settings":    {"Settings", "Настройки"},
	"nav.diagnostics": {"Diagnostics", "Диагностика"},
	"nav.hint":        {"Ctrl+1…6 sections · Ctrl+± text size · Ctrl+Q quit", "Ctrl+1…6 разделы · Ctrl+± размер текста · Ctrl+Q выход"},

	"tray.tooltip":     {"Companion", "Companion"},
	"tray.open":        {"Open", "Открыть"},
	"tray.open.setup":  {"Open setup", "Открыть настройку"},
	"tray.diagnostics": {"Diagnostics", "Диагностика"},
	"tray.quit":        {"Quit", "Выход"},

	"wizard.title": {"Companion setup", "Настройка Companion"},
	"wizard.back":  {"Back to setup", "Назад к настройке"},

	"home.heading":  {"Companion", "Companion"},
	"home.body":     {"Voice and screen assistant. Talk, chats and history arrive in later versions.", "Голосовой и экранный помощник. Разговор, чаты и история появятся в следующих версиях."},
	"home.status":   {"Ready", "Готов"},
	"home.settings": {"Open settings", "Открыть настройки"},
	"home.about":    {"About", "О программе"},

	"about.title":  {"About Companion", "О Companion"},
	"about.body":   {"Version %s. All data stays on this device.", "Версия %s. Все данные остаются на этом устройстве."},
	"dialog.close": {"Close (Esc)", "Закрыть (Esc)"},

	"placeholder.body": {"This section arrives in a later version.", "Этот раздел появится в следующих версиях."},

	"settings.heading":   {"Settings", "Настройки"},
	"settings.saveError": {"Could not save the setting. Previous value kept.", "Не удалось сохранить настройку. Оставлено прежнее значение."},
	"settings.later":     {"Configured in a later version.", "Настраивается в следующих версиях."},
	"settings.notSet":    {"not configured", "не настроено"},

	"group.voice":        {"Voice", "Голос"},
	"group.models":       {"Models", "Модели"},
	"group.tts":          {"Speech output", "Озвучивание"},
	"group.screen":       {"Screen", "Экран"},
	"group.memory":       {"Memory", "Память"},
	"group.agents":       {"Agents", "Агенты"},
	"group.integrations": {"Integrations", "Интеграции"},
	"group.privacy":      {"Privacy", "Приватность"},
	"group.platform":     {"Platform", "Система"},

	"voice.hotkeyMode":   {"Talk hotkey mode", "Режим клавиши разговора"},
	"voice.hold":         {"Hold to talk", "Удерживать"},
	"voice.toggle":       {"Press to start / stop", "Нажать для старта / стопа"},
	"voice.handsfree":    {"Hands-free", "Без рук"},
	"voice.language":     {"Speech language: %s", "Язык речи: %s"},
	"voice.languageAuto": {"auto-detect", "автоопределение"},

	"models.talk":    {"Talk", "Разговор"},
	"models.vision":  {"Vision", "Зрение"},
	"models.agent":   {"Agent", "Агент"},
	"models.cleanup": {"Dictation cleanup", "Очистка диктовки"},
	"models.fast":    {"Fast", "Быстрая"},
	"models.deep":    {"Deep", "Глубокая"},

	"tts.speed":  {"Speech speed: %.2f×", "Скорость речи: %.2f×"},
	"tts.slower": {"Slower", "Медленнее"},
	"tts.faster": {"Faster", "Быстрее"},

	"screen.target":     {"Screen to capture", "Какой экран снимать"},
	"screen.active":     {"Active display", "Активный дисплей"},
	"screen.all":        {"All displays", "Все дисплеи"},
	"screen.keepShots":  {"Keep screenshots in history", "Хранить скриншоты в истории"},
	"memory.enabled":    {"Remember facts between conversations", "Запоминать факты между разговорами"},
	"privacy.saveText":  {"Save transcripts", "Сохранять расшифровки"},
	"privacy.shareDiag": {"Allow attaching diagnostics to bug reports", "Разрешить прикладывать диагностику к отчётам"},

	"platform.uiLang": {"Interface language", "Язык интерфейса"},
	"platform.login":  {"Launch at login", "Запускать при входе"},
	"platform.tray":   {"Show tray icon", "Показывать значок в трее"},

	"diag.heading": {"Diagnostics", "Диагностика"},
	"diag.run":     {"Run again", "Проверить снова"},
	"diag.running": {"Checking the system…", "Проверяем систему…"},
	"diag.failed":  {"Detection finished with errors; results may be partial.", "Проверка завершилась с ошибками; результаты могут быть неполными."},
	"diag.system":  {"System: %s, session %s, desktop %s", "Система: %s, сеанс %s, окружение %s"},
}

// T returns the text for key in l (see i18n.T).
func T(l Lang, key string) string { return i18n.T(l, key) }

// Tf is T with fmt.Sprintf arguments.
func Tf(l Lang, key string, args ...any) string { return i18n.Tf(l, key, args...) }

func langOf(code string) Lang { return i18n.Of(code) }
