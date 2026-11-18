package voicedesk

import (
	"strings"
	"unicode"
)

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
		"haan bhejdo", "haa bhej do", "han bhej do", "ha ji", "hanji", "haanji",
		"okay", "ok", "yeah", "yep", "sure", "yes please", "go ahead", "theek hai", "thik hai", "ji", "bhejo", "haan haan",
		"do it", "kar do", "haan kar do", "yes do it", "ok send", "okay send it",
		"ہاں", "جی ہاں", "ہاں بھیجو", "ہاں جی", "हाँ", "हां", "हाँ भेजो", "हां भेजो", "हाँ जी", "जी हाँ", "भेज दो")
	noAnswers = foldPhrases("no", "cancel", "nahin", "nahi", "nahee", "rehne do", "rahne do", "rehene do", "mat bhejo",
		"nope", "no thanks", "mat karo", "nahin bhejo", "stop it", "نہیں", "رہنے دو", "مت بھیجو", "नहीं", "नहि", "रहने दो", "मत भेजो")
	// ordinal answers, value = index
	choiceAnswers = map[string]int{}
)

func init() {
	for i, forms := range [][]string{
		{"first", "the first", "first one", "the first one", "pehla", "pahla", "pehla wala", "pahla wala", "pehli", "pahli", "pehli wali", "पहला", "पहली", "पहला वाला"},
		{"second", "the second", "second one", "the second one", "doosra", "dusra", "doosra wala", "doosri", "doosri wali", "दूसरा", "दूसरी", "दूसरा वाला"},
		{"third", "the third", "third one", "the third one", "teesra", "tisra", "teesra wala", "tisra wala", "teesri", "tisri", "teesri wali", "तीसरा", "तीसरी", "तीसरा वाला"},
	} {
		for _, f := range forms {
			choiceAnswers[answerWords(f)] = i
		}
	}
}

// foldPhrases builds an answer table. Despite the name nothing is folded
// for answers: a row is its words, lowercased, punctuation dropped.
func foldPhrases(ps ...string) map[string]bool {
	m := make(map[string]bool, len(ps))
	for _, p := range ps {
		m[answerWords(p)] = true
	}
	return m
}

// answerWords is a phrase as its lowercased words, nothing else changed.
func answerWords(s string) string {
	ws := splitWords(s)
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, strings.ToLower(w.raw))
	}
	return strings.Join(out, " ")
}

// answerText is the sentence as compared against the answer table: its
// lowercased words (no spelling folding), a leading "jarvis" dropped.
func answerText(sentence string) string {
	t := answerWords(sentence)
	if t == "jarvis" {
		return ""
	}
	return strings.TrimPrefix(t, "jarvis ")
}

// classifyAnswer says whether a sentence is a yes, a no or an ordinal choice
// (index). Names as a choice are handled by the caller, which knows the
// candidates.
func classifyAnswer(sentence string) (kind string, index int) {
	if hasQuestionMark(sentence) {
		return AnswerNone, -1 // "yes?", "Yes?!", "ہاں؟" are a question back, not consent
	}
	t := answerText(sentence)
	switch {
	case t == "":
		return AnswerNone, -1
	case yesAnswers[t]:
		return AnswerYes, -1
	case noAnswers[t]:
		return AnswerNo, -1
	}
	if i, ok := choiceAnswers[t]; ok {
		return AnswerChoice, i
	}
	return AnswerNone, -1
}

// Answers are matched EXACTLY against the table (after case, punctuation and
// a leading wake name are dropped): every accepted spelling is a row of the
// table in docs/voice-desk.md. No fuzzy or phonetic matching for answers:
// "sorry" must never be "sure", "ha" never "haan".

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

// questionMarks: ASCII, full-width, small and vertical forms, Arabic/Urdu,
// double, interrobang, inverted, reversed, and the question mark emoji.
const questionMarks = "?？﹖︖؟⁇‽¿⸮⁈⁉❓❔"

// hasQuestionMark: a question mark anywhere in the answer, after zero-width
// and other format characters are taken out ("yes?\u200b", "ہاں؟\u200f").
func hasQuestionMark(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cf, r) {
			continue
		}
		if strings.ContainsRune(questionMarks, r) {
			return true
		}
	}
	return false
}
