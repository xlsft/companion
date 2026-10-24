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

	"nav.home":        {"Chat", "Чат"},
	"nav.history":     {"History", "История"},
	"nav.agents":      {"Agents", "Агенты"},
	"nav.skills":      {"Skills", "Навыки"},
	"nav.settings":    {"Settings", "Настройки"},
	"nav.diagnostics": {"Diagnostics", "Диагностика"},
	"nav.hint":        {"Ctrl+1…6 sections · Ctrl+N new chat · Ctrl+± text size · Ctrl+Q quit", "Ctrl+1…6 разделы · Ctrl+N новый чат · Ctrl+± размер текста · Ctrl+Q выход"},

	"tray.tooltip":     {"Companion", "Companion"},
	"tray.open":        {"Open", "Открыть"},
	"tray.open.setup":  {"Open setup", "Открыть настройку"},
	"tray.diagnostics": {"Diagnostics", "Диагностика"},
	"tray.quit":        {"Quit", "Выход"},

	"wizard.title": {"Companion setup", "Настройка Companion"},
	"wizard.back":  {"Back to setup", "Назад к настройке"},

	"home.about":      {"About Companion", "О программе"},
	"sidebar.newChat": {"New chat", "Новый чат"},

	"chat.title.new":           {"New chat", "Новый чат"},
	"chat.status.offline":      {"Chat not connected", "Чат не подключён"},
	"chat.status.ready":        {"Ready to listen", "Готов слушать"},
	"chat.status.readyText":    {"Ready", "Готов"},
	"chat.status.listening":    {"Listening…", "Слушаю…"},
	"chat.status.transcribing": {"Recognising speech…", "Распознаю речь…"},
	"chat.status.thinking":     {"Thinking…", "Думаю…"},
	"chat.status.responding":   {"Answering…", "Отвечаю…"},
	"chat.status.speaking":     {"Speaking…", "Говорю…"},
	"chat.status.awaiting":     {"Waiting for your answer", "Жду вашего ответа"},
	"chat.stop":                {"Stop", "Стоп"},
	"chat.via.voice":           {"by voice", "голосом"},
	"chat.via.text":            {"typed", "текстом"},
	"chat.interrupted":         {"Stopped", "Остановлено"},
	"chat.failed":              {"Couldn't get an answer", "Не удалось получить ответ"},
	"chat.failedCode":          {"code: %s", "код: %s"},
	"chat.placeholder":         {"Ask about what's on your screen…", "Спросите про то, что на экране…"},
	"chat.placeholderOff":      {"Chat is unavailable", "Чат недоступен"},
	"chat.enterHint":           {"Enter — send", "Enter — отправить"},
	"chat.send":                {"Send", "Отправить"},
	"chat.speak":               {"Speak", "Говорить"},
	"chat.speakStop":           {"Finish speaking and send", "Закончить и отправить"},
	"chat.voiceOff":            {"Voice input unavailable", "Голосовой ввод недоступен"},
	"chat.hint.hotkey":         {"%s — ask by voice", "%s — спросить голосом"},
	"chat.hint.local":          {"Audio is recognised on this computer", "Аудио распознаётся на компьютере"},
	"chat.hint.noVoice":        {"Voice input is unavailable — see Diagnostics", "Голосовой ввод недоступен — см. Диагностику"},
	"chat.hint.offline":        {"Nothing is sent while the chat is not connected", "Пока чат не подключён, ничего не отправляется"},
	"chat.empty.title":         {"Ask about your screen", "Спросите про экран"},
	"chat.empty.body.voice":    {"Type a question below or say it — the answer appears here.", "Напишите вопрос ниже или скажите его — ответ появится здесь."},
	"chat.empty.body.text":     {"Type a question below — the answer appears here.", "Напишите вопрос ниже — ответ появится здесь."},
	"chat.off.title":           {"Chat isn't connected", "Чат не подключён"},
	"chat.off.body":            {"This build has no assistant session yet, so messages can't be sent. Settings and Diagnostics work as usual.", "В этой сборке ещё нет сеанса помощника, поэтому сообщения не отправляются. Настройки и диагностика работают как обычно."},
	"chat.error.send":          {"Couldn't send the message. Try again.", "Не удалось отправить сообщение. Попробуйте ещё раз."},
	"chat.error.voice":         {"Couldn't start or finish recording.", "Не удалось начать или закончить запись."},
	"chat.error.stop":          {"Couldn't stop the request.", "Не удалось остановить запрос."},
	"chat.error.newChat":       {"Couldn't start a new chat.", "Не удалось начать новый чат."},

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
	"diag.system":  {"System: %s", "Система: %s"},
}

// T returns the text for key in l (see i18n.T).
func T(l Lang, key string) string { return i18n.T(l, key) }

// Tf is T with fmt.Sprintf arguments.
func Tf(l Lang, key string, args ...any) string { return i18n.Tf(l, key, args...) }

func langOf(code string) Lang { return i18n.Of(code) }
