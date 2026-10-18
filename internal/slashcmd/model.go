package slashcmd

import (
	"regexp"
	"strings"
)

// modelSpec is a model id, alias or display name reduced to what tells two
// models apart: the family ("opus", "sonnet", "haiku", ...), the version
// numbers, and the 1M-context variant. "claude-opus-5-5[1m]",
// "opus[1m]" and "Opus 5.5 (1M context)" share family opus and oneM; the
// first and the last also share version 5.5.
type modelSpec struct {
	family  string
	version []string // empty for an alias ("opus", "haiku")
	oneM    bool
}

var (
	oneMSuffix = regexp.MustCompile(`\s*(\[1m\]|\(1m context\))\s*`)
	// dateToken is a release date in a full id: claude-haiku-4-5-20251001.
	dateToken = regexp.MustCompile(`^\d{6,}$`)
	numToken  = regexp.MustCompile(`^\d+$`)
	// labelNoise is picker decoration after the name: "(recommended)",
	// "(default)", "· current".
	labelNoise = regexp.MustCompile(`\s*(\((recommended|default|current)\)|·.*)$`)
)

// parseModelSpec reduces s to a modelSpec. ok is false when s names no
// family (empty, or only numbers).
func parseModelSpec(s string) (modelSpec, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	var spec modelSpec
	if oneMSuffix.MatchString(s) {
		spec.oneM = true
		s = oneMSuffix.ReplaceAllString(s, " ")
	}
	s = labelNoise.ReplaceAllString(strings.TrimSpace(s), "")
	s = strings.NewReplacer("-", " ", ".", " ", "_", " ", "(", " ", ")", " ").Replace(s)
	for _, tok := range strings.Fields(s) {
		switch {
		case tok == "claude", dateToken.MatchString(tok):
		case numToken.MatchString(tok):
			spec.version = append(spec.version, tok)
		case spec.family == "":
			spec.family = tok
		default:
			// A second word ("latest") is not part of the identity.
		}
	}
	return spec, spec.family != ""
}

// matches reports whether display (a picker label or a "Set model to"
// name) is the model want names: the same family token (whole token, so
// "s" never matches sonnet), the same 1M variant, and, when want has a
// version, exactly that version ("opus-4" is not Opus 4.6). An alias
// matches every version of its family.
func (want modelSpec) matches(display modelSpec) bool {
	if want.family == "" || want.family != display.family || want.oneM != display.oneM {
		return false
	}
	if len(want.version) == 0 {
		return true
	}
	return strings.Join(want.version, ".") == strings.Join(display.version, ".")
}

// isDefaultModel reports the "default" alias, which Claude resolves itself.
func isDefaultModel(s string) bool {
	spec, ok := parseModelSpec(s)
	return ok && spec.family == "default" && len(spec.version) == 0
}

// modelMatches reports whether display names the model requested as arg.
// "default" matches any model Claude reports switching to.
func modelMatches(arg, display string) bool {
	if isDefaultModel(arg) {
		return strings.TrimSpace(display) != ""
	}
	want, ok := parseModelSpec(arg)
	if !ok {
		return false
	}
	got, ok := parseModelSpec(display)
	return ok && want.matches(got)
}
