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

	"nav.home":     {"Chat", "Чат"},
	"nav.history":  {"History", "История"},
	"nav.agents":   {"Agents", "Агенты"},
	"nav.skills":   {"Skills", "Навыки"},
	"nav.settings": {"Settings", "Настройки"},
	"nav.soon":     {"Soon", "Скоро"},
	"nav.hint":     {"Ctrl+1 chat · Ctrl+2 settings · Ctrl+N new chat · Ctrl+± text size · Ctrl+Q quit", "Ctrl+1 чат · Ctrl+2 настройки · Ctrl+N новый чат · Ctrl+± размер текста · Ctrl+Q выход"},

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
	"chat.hint.noVoice":        {"Voice input is unavailable — see Settings › Diagnostics", "Голосовой ввод недоступен — см. Настройки › Диагностика"},
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

	"settings.heading":         {"Settings", "Настройки"},
	"settings.saveError":       {"Could not save the setting", "Не удалось сохранить настройку"},
	"settings.saveError.d":     {"The previous value is kept.", "Оставлено прежнее значение."},
	"settings.sec.general":     {"General", "Общие"},
	"settings.sec.voice":       {"Voice", "Голос"},
	"settings.sec.screen":      {"Screen", "Экран"},
	"settings.sec.privacy":     {"Privacy", "Приватность"},
	"settings.sec.diagnostics": {"Diagnostics", "Диагностика"},

	"platform.uiLang":   {"Interface language", "Язык интерфейса"},
	"platform.uiLang.d": {"Applies right away", "Применяется сразу"},
	"platform.tray":     {"Show tray icon", "Показывать значок в трее"},
	"platform.tray.d":   {"Applies after a restart; without it, closing the window quits Companion", "Применится после перезапуска; без значка закрытие окна завершает Companion"},
	"platform.login":    {"Launch at login", "Запускать при входе"},
	"platform.login.d":  {"Start Companion when you sign in", "Запускать Companion при входе в систему"},

	"voice.hotkeyMode":   {"Talk hotkey", "Клавиша разговора"},
	"voice.hotkeyMode.d": {"How the hotkey starts and ends a request", "Как клавиша начинает и заканчивает запрос"},
	"voice.hold":         {"Hold", "Удержание"},
	"voice.toggle":       {"Toggle", "Переключение"},
	"voice.handsfree":    {"Hands-free", "Без рук"},
	"voice.language":     {"Speech language", "Язык речи"},
	"voice.language.d":   {"The language the recogniser expects", "Язык, который ожидает распознавание"},
	"voice.languageAuto": {"Auto-detect", "Автоопределение"},
	"tts.speed":          {"Speech speed", "Скорость речи"},
	"tts.speedValue":     {"%.2f×", "%.2f×"},

	"screen.target":      {"Screen to capture", "Какой экран снимать"},
	"screen.target.d":    {"What Companion looks at when you ask", "На что смотрит Companion в момент вопроса"},
	"screen.active":      {"Active display", "Активный дисплей"},
	"screen.all":         {"All displays", "Все дисплеи"},
	"screen.keepShots":   {"Keep screenshots in history", "Хранить снимки в истории"},
	"screen.keepShots.d": {"Otherwise only the text of a conversation is kept", "Иначе в истории остаётся только текст"},

	"privacy.saveText":    {"Save conversations", "Сохранять разговоры"},
	"privacy.saveText.d":  {"Keep transcripts and answers on this computer", "Хранить расшифровки и ответы на этом компьютере"},
	"memory.enabled":      {"Remember facts between conversations", "Запоминать факты между разговорами"},
	"memory.enabled.d":    {"Facts you mention are used in later answers", "Упомянутые факты используются в следующих ответах"},
	"privacy.shareDiag":   {"Attach diagnostics to bug reports", "Прикладывать диагностику к отчётам об ошибках"},
	"privacy.shareDiag.d": {"Technical codes only, never your content", "Только технические коды, без вашего содержимого"},
	"privacy.note":        {"Everything stays on this computer; nothing is sent to us.", "Всё хранится на этом компьютере; нам ничего не отправляется."},

	"diag.heading":        {"Diagnostics", "Диагностика"},
	"diag.lead":           {"What this computer lets Companion do, and what it uses instead where something is missing.", "Что этот компьютер позволяет Companion и чем он заменяет то, чего нет."},
	"diag.run":            {"Run again", "Проверить снова"},
	"diag.copy":           {"Copy report", "Скопировать отчёт"},
	"diag.copied":         {"Copied", "Скопировано"},
	"diag.running":        {"Checking the system…", "Проверяем систему…"},
	"diag.failed":         {"Detection finished with errors", "Проверка завершилась с ошибками"},
	"diag.failed.d":       {"Results may be partial; the copied report has the details.", "Результаты могут быть неполными; подробности — в скопированном отчёте."},
	"diag.tile.system":    {"System", "Система"},
	"diag.tile.works":     {"Works", "Работает"},
	"diag.tile.attention": {"Limited or missing", "Ограничено или нет"},
	"diag.ofTotal":        {"%d of %d", "%d из %d"},
	"diag.count":          {"%d", "%d"},
	"diag.works.sub":      {"capabilities available", "возможностей доступно"},
	"diag.attention.none": {"nothing to fix", "исправлять нечего"},
	"diag.attention.some": {"Companion uses a fallback; see the table", "Companion использует замену; см. таблицу"},
	"diag.checkedAt":      {"checked at %s", "проверено в %s"},
	"diag.reportNote":     {"The copied report adds technical details for support. It never contains your requests, screenshots, audio or keys.", "Скопированный отчёт содержит технические подробности для поддержки. В нём нет ваших запросов, снимков, аудио и ключей."},
}

// T returns the text for key in l (see i18n.T).
func T(l Lang, key string) string { return i18n.T(l, key) }

// Tf is T with fmt.Sprintf arguments.
func Tf(l Lang, key string, args ...any) string { return i18n.Tf(l, key, args...) }

func langOf(code string) Lang { return i18n.Of(code) }
