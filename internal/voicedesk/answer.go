package voicedesk

import "strings"

// Answer kinds to a pending question.
const (
	AnswerNone   = ""
	AnswerYes    = "yes"
	AnswerNo     = "no"
	AnswerChoice = "choice"
)

// The yes and no lists are WHOLE sentences (CONTRACT.md section 3): a "yes"
// inside a longer sentence is not a yes. Compared folded, after the
// punctuation is gone and a leading wake name is dropped.
var (
	yesAnswers = foldPhrases("yes", "yes send", "yes send it", "haan", "haan bhejo", "haan ji", "ji haan",
		"send it", "bhej do", "yes switch", "haa", "han", "haa bhejo", "han bhejo", "haan bhej do", "bhejdo", "han ji", "ji han",
		"okay", "ok", "yeah", "yep", "sure", "yes please", "go ahead", "theek hai", "thik hai", "ji", "bhejo", "haan haan",
		"ہاں", "جی ہاں", "ہاں بھیجو", "ہاں جی", "हाँ", "हां", "हाँ भेजो", "हां भेजो", "हाँ जी", "जी हाँ", "भेज दो")
	noAnswers = foldPhrases("no", "cancel", "nahin", "nahi", "nahee", "rehne do", "rahne do", "mat bhejo",
		"nope", "no thanks", "mat karo", "nahin bhejo", "stop it", "نہیں", "رہنے دو", "مت بھیجو", "नहीं", "नहि", "रहने दो", "मत भेजो")
	// ordinal answers, value = index
	choiceAnswers = map[string]int{}
)

func init() {
	for i, forms := range [][]string{
		{"first", "the first", "first one", "the first one", "pehla", "pehla wala", "pehli", "pehli wali", "पहला", "पहली", "पहला वाला"},
		{"second", "the second", "second one", "the second one", "doosra", "dusra", "doosra wala", "doosri", "doosri wali", "दूसरा", "दूसरी", "दूसरा वाला"},
		{"third", "the third", "third one", "the third one", "teesra", "tisra", "teesra wala", "teesri", "teesri wali", "तीसरा", "तीसरी", "तीसरा वाला"},
	} {
		for _, f := range forms {
			choiceAnswers[foldPhrase(f)] = i
		}
	}
}

func foldPhrases(ps ...string) map[string]bool {
	m := make(map[string]bool, len(ps))
	for _, p := range ps {
		m[foldPhrase(p)] = true
	}
	return m
}

// answerText is the sentence as compared against the answer lists: folded
// words, a leading "jarvis" dropped.
func answerText(sentence string) string {
	ws := foldedWords(splitWords(sentence))
	if len(ws) > 0 && ws[0] == fold("jarvis") {
		ws = ws[1:]
	}
	return strings.Join(ws, " ")
}

// classifyAnswer says whether a sentence is a yes, a no or an ordinal choice
// (index). Names as a choice are handled by the caller, which knows the
// candidates.
func classifyAnswer(sentence string) (kind string, index int) {
	if strings.HasSuffix(strings.TrimSpace(sentence), "?") {
		return AnswerNone, -1 // "yes?" is a question back, not consent
	}
	t := answerText(sentence)
	switch {
	case t == "":
		return AnswerNone, -1
	case inAnswers(t, yesAnswers):
		return AnswerYes, -1
	case inAnswers(t, noAnswers):
		return AnswerNo, -1
	}
	for form, i := range choiceAnswers {
		if answerEqual(t, form) {
			return AnswerChoice, i
		}
	}
	return AnswerNone, -1
}

func inAnswers(t string, set map[string]bool) bool {
	if set[t] {
		return true
	}
	for form := range set {
		if answerEqual(t, form) {
			return true
		}
	}
	return false
}

// answerEqual: the whole sentence is the answer, word for word; a word of
// four letters or more may be spelled another way the listener writes it
// ("rahne"/"rehne", "pahla"/"pehla", "bhejdo"/"bhej do" is listed). Short
// words must be equal: "ho" is never "haan".
func answerEqual(t, form string) bool {
	if t == form {
		return true
	}
	a, b := strings.Fields(t), strings.Fields(form)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] == b[i] {
			continue
		}
		if len(a[i]) < 4 || len(b[i]) < 4 || skeleton(a[i]) != skeleton(b[i]) || phoneticKey(a[i]) != phoneticKey(b[i]) {
			return false
		}
	}
	return true
}

// Risky words in a tell message (CONTRACT.md section 4 rule 3). Any of them
// makes a yes necessary even for the session he is on.
var riskyWords = foldSet(
	"delete", "deleted", "remove", "rm", "trash", "force", "push", "merge", "deploy", "release", "install",
	"uninstall", "restart", "reboot", "kill", "pay", "payment", "publish", "drop", "wipe", "reset", "destroy",
	"mail", "email", "e-mail",
	// Roman Hindi / Devanagari spellings as a listener writes them
	"hata", "hatao", "mita", "mitao", "dilit", "marj", "rilij", "instol", "ristart", "pement", "paisa", "paise", "band",
	"imel", "imail", "imeil", "revert", "rollback", "erase", "format", "purge", "truncate", "shutdown",
)

// risky reports whether a message holds a risky word.
func risky(message string) bool {
	for _, w := range splitWords(message) {
		if riskyWords[w.folded] {
			return true
		}
		// "deleting", "merged", "pushes", "installation": stem prefix
		// "deleting", "reinstall", "republish": a risky stem anywhere in a word
		for _, stem := range []string{"delet", "remov", "deplo", "releas", "instal", "restart", "publish", "revert", "reboot"} {
			if strings.Contains(w.roman, stem) {
				return true
			}
		}
		// "merged", "pushes", "payment": short stems only at the start
		for _, stem := range []string{"merg", "push", "pay", "kill", "wipe"} {
			if strings.HasPrefix(w.roman, stem) {
				return true
			}
		}
	}
	return false
}
