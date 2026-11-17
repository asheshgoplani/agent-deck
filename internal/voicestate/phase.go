package voicestate

// The six phase words of the voice desk contract. Every surface that shows
// or reports the voice phase (voice state --json, the phone, the Mac app)
// uses exactly these.
const (
	PhaseListening = "listening"
	PhaseHearing   = "hearing"
	PhaseThinking  = "thinking"
	PhaseSpeaking  = "speaking"
	PhaseResting   = "resting"
	PhaseOff       = "off"
)

// Phases lists the six words.
var Phases = []string{PhaseListening, PhaseHearing, PhaseThinking, PhaseSpeaking, PhaseResting, PhaseOff}

// MapPhase maps the daemon's raw phase word to one of the six.
func MapPhase(raw string) string {
	switch raw {
	case PhaseListening, PhaseThinking, PhaseSpeaking, PhaseResting:
		return raw
	case PhaseHearing, "transcribing":
		return PhaseHearing
	case PhaseOff, "":
		return PhaseOff
	default:
		// muted, paused (call), meeting and anything new: the mic is not
		// taking commands, which is "resting".
		return PhaseResting
	}
}

// PhaseWord is the contract phase of a polled state; nil or "on": false is off.
func (s *State) PhaseWord() string {
	if s == nil || !s.On {
		return PhaseOff
	}
	return MapPhase(s.phase())
}
