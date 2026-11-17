package voicedesk

// Phase 2 intents (CONTRACT-PHASE2.md).
const (
	IntentLaunch   = "launch"
	IntentStop     = "stop"
	IntentRestart  = "restart"
	IntentArchive  = "archive"
	IntentSearch   = "search"
	IntentWorkers  = "workers"
	IntentNext     = "next"
	IntentRefuse   = "refuse" // delete / remove a session: never by voice
	IntentTodoAdd  = "todo_add"
	IntentTodoList = "todo_list"
	IntentTodoDone = "todo_done"
)

type patternAdder struct{ ps []pattern }

func (a *patternAdder) add(intent, lang string, soft, all bool, specs ...string) {
	for _, s := range specs {
		p := compile(intent, lang, soft, s)
		p.all = all
		a.ps = append(a.ps, p)
	}
}

// grammarEarly runs before the phase-1 forms: these sentences would
// otherwise read as status ("deck ke workers kya kar rahe hain") or find
// ("find where we talked about the invoice").
func grammarEarly() []pattern {
	var a patternAdder
	grammarTodos(&a)
	// workers of a session
	a.add(IntentWorkers, LangEN, true,
		false,
		"what are [the] {name} workers doing", "what are the workers of {name} doing",
		"how are [the] {name} workers doing", "[show|list] [me] [the] workers of {name}",
		"what are [the] {name} children doing",
	)
	a.add(IntentWorkers, LangHI, true, false,
		"{name} ke workers kya kar rahe hai|hain", "{name} ke workers batao|dikhao|sunao",
		"{name} ke workers ka haal|status batao",
	)
	// search what was said
	a.add(IntentSearch, LangEN, false, false,
		"where|when did we talk|speak about {msg}", "where|when did we discuss {msg}",
		"search what was said about {msg}", "search [the] conversations|transcripts for|about {msg}",
		"find where we talked|spoke about {msg}", "find where we discussed {msg}",
		"what did we say about {msg}",
	)
	a.add(IntentSearch, LangHI, false, false,
		"{msg} ki|ke baat kahan|kab hui [thi]", "{msg} ke bare|baare mein kahan|kab baat hui [thi]",
		"{msg} ke bare|baare mein kya baat hui [thi]",
	)
	// the next one that needs me
	a.add(IntentNext, LangEN, false, false,
		"take me to the next one", "next one that needs me", "go to the next one [that needs me]",
		"take me to the next waiting session", "next waiting session", "who is next",
		"show me the next one [that needs me]",
	)
	a.add(IntentNext, LangHI, false, false,
		"agla|agle wala|wale kholo|dikhao", "agle wale pe|par chalo|jao", "agli session kholo|dikhao",
		"agla kaun hai",
	)
	// launch a new session
	for _, verb := range []string{"start", "launch", "create", "open", "make"} {
		a.add(IntentLaunch, LangEN, false, false,
			verb+" [a] new session called|named {title} in|under [the] {group} [group|folder]",
			verb+" [a] new session called|named {title}",
		)
	}
	a.add(IntentLaunch, LangEN, false, false,
		"new session called|named {title} in|under [the] {group} [group|folder]", "new session called|named {title}",
	)
	a.add(IntentLaunch, LangHI, false, false,
		"[ek] nayi|naya|nai session|seshan shuru|start|banao|kholo [karo|kar] [do] naam|name {title} {group} mein",
		"[ek] nayi|naya|nai session|seshan shuru|start|banao|kholo [karo|kar] [do] naam|name {title}",
		"{group} mein [ek] nayi|naya|nai session|seshan shuru|start|banao|kholo [karo|kar] [do] naam|name {title}",
		"{title} naam ki|ka [ek] nayi|naya|nai session|seshan shuru|start|banao|kholo [karo|kar] [do]",
		"{group} mein {title} naam ki|ka [ek] nayi|naya|nai session|seshan shuru|start|banao|kholo [karo|kar] [do]",
	)
	// stop / restart / archive, all sessions the name fits
	a.add(IntentStop, LangEN, true, true, "stop all [the|my] {name}", "kill all [the|my] {name}")
	a.add(IntentRestart, LangEN, true, true, "restart all [the|my] {name}")
	a.add(IntentArchive, LangEN, true, true, "archive all [the|my] {name}")
	a.add(IntentStop, LangHI, true, true, "sab|saare|sare|saari {name} band|rok [karo|kar|kardo] [do]", "{name} sab band|rok [karo|kar|kardo] [do]")
	a.add(IntentRestart, LangHI, true, true, "sab|saare|sare|saari {name} restart [karo|kar|kardo] [do]")
	a.add(IntentArchive, LangHI, true, true, "sab|saare|sare|saari {name} archive [karo|kar|kardo] [do]")
	return a.ps
}

// grammarLate runs after the phase-1 forms.
func grammarLate() []pattern {
	var a patternAdder
	// one session (an unknown name is not mine: "stop listening" is the daemon's)
	a.add(IntentStop, LangEN, true, false, "stop [the|my] {name} [session]", "kill [the|my] {name} session")
	a.add(IntentRestart, LangEN, true, false, "restart [the|my] {name} [session]")
	a.add(IntentArchive, LangEN, true, false, "archive [the|my] {name} [session]")
	a.add(IntentStop, LangHI, true, false, "{name} [ko] band|rok [karo|kar|kardo] [do]", "{name} [ko] stop [karo|kar|kardo] [do]")
	a.add(IntentRestart, LangHI, true, false, "{name} [ko] restart [karo|kar|kardo] [do]", "{name} [ko] phir|fir|dobara [se] shuru|start [karo|kar|kardo] [do]")
	a.add(IntentArchive, LangHI, true, false, "{name} [ko] archive [karo|kar|kardo] [do]")
	// delete / remove: refused by name, never done by voice
	a.add(IntentRefuse, LangEN, true, false, "delete|remove [the|my] {name} session", "delete|remove [the|my] session {name}")
	a.add(IntentRefuse, LangHI, true, false, "{name} session delete|hata|mita [karo|kar|kardo|do] [do]")
	return a.ps
}

// daemonObjects are what the voice daemon itself stops, restarts or
// closes ("stop listening", "meeting band karo"): never a session name for
// a lifecycle verb, decided before any listing.
var daemonObjects = foldSet("listening", "talking", "speaking", "reading", "meeting", "voice", "mic", "music",
	"audio", "sound", "recording", "front", "mode", "glasses", "call", "camera", "it", "this", "that", "everything",
	"all", "sab", "kuch", "ye", "yeh", "isse", "isko", "of")

func lifecycleIntent(intent string) bool {
	return intent == IntentStop || intent == IntentRestart || intent == IntentArchive || intent == IntentRefuse
}
