package walkthrough

import (
	"strings"
	"unicode"
)

// phrases are the utterances that control a walkthrough, in Russian and
// English, as speech recognition writes them once lower-cased and stripped
// of punctuation. Only a whole short utterance counts, so a question that
// merely contains "stop" or "дальше" still goes to the model.
var phrases = map[string]Command{
	"дальше": CmdNext, "далее": CmdNext, "следующий": CmdNext, "следующий шаг": CmdNext,
	"вперёд": CmdNext, "вперед": CmdNext, "готово": CmdNext, "сделал": CmdNext, "сделала": CmdNext,
	"сделано": CmdNext, "что дальше": CmdNext, "а дальше": CmdNext,
	"next": CmdNext, "next step": CmdNext, "done": CmdNext, "i'm done": CmdNext, "im done": CmdNext,
	"what's next": CmdNext, "whats next": CmdNext, "go ahead": CmdNext, "forward": CmdNext,

	"назад": CmdPrev, "предыдущий": CmdPrev, "предыдущий шаг": CmdPrev, "шаг назад": CmdPrev,
	"вернись": CmdPrev, "вернись назад": CmdPrev, "верни": CmdPrev,
	"back": CmdPrev, "go back": CmdPrev, "previous": CmdPrev, "previous step": CmdPrev, "step back": CmdPrev,

	"повтори": CmdRepeat, "повтори шаг": CmdRepeat, "повтори ещё раз": CmdRepeat, "повтори еще раз": CmdRepeat,
	"повторить": CmdRepeat, "ещё раз": CmdRepeat, "еще раз": CmdRepeat, "не понял": CmdRepeat, "не поняла": CmdRepeat,
	"repeat": CmdRepeat, "repeat that": CmdRepeat, "again": CmdRepeat, "say again": CmdRepeat,
	"say that again": CmdRepeat, "one more time": CmdRepeat,

	"пауза": CmdPause, "подожди": CmdPause, "погоди": CmdPause, "постой": CmdPause, "секунду": CmdPause,
	"pause": CmdPause, "wait": CmdPause, "hold on": CmdPause, "one second": CmdPause, "one sec": CmdPause,

	"продолжи": CmdResume, "продолжай": CmdResume, "продолжить": CmdResume, "продолжим": CmdResume,
	"resume": CmdResume, "continue": CmdResume, "go on": CmdResume, "carry on": CmdResume,

	"стоп": CmdStop, "хватит": CmdStop, "закончи": CmdStop, "закончить": CmdStop, "закончим": CmdStop,
	"отмена": CmdStop, "отмени": CmdStop, "завершить": CmdStop, "заверши": CmdStop, "прекрати": CmdStop,
	"stop": CmdStop, "cancel": CmdStop, "quit": CmdStop, "end": CmdStop, "exit": CmdStop, "enough": CmdStop,
	"stop the walkthrough": CmdStop,
}

// fillers may surround a command without changing it.
var fillers = map[string]bool{
	"пожалуйста": true, "давай": true, "ну": true, "так": true, "ок": true, "окей": true, "ладно": true,
	"хорошо": true, "please": true, "ok": true, "okay": true, "now": true, "so": true, "alright": true,
}

// ParseIntent recognises a walkthrough command in an utterance.
func ParseIntent(text string) (Command, bool) {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(strings.ReplaceAll(text, "’", "'")), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\''
	}) {
		w = strings.Trim(w, "'")
		if w != "" && !fillers[w] {
			words = append(words, w)
		}
	}
	if len(words) == 0 || len(words) > 4 {
		return "", false
	}
	cmd, ok := phrases[strings.Join(words, " ")]
	return cmd, ok
}
