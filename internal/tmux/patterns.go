package tmux

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var patternLog = logging.ForComponent(logging.CompStatus)

// RawPatterns holds string-form patterns before compilation.
// Patterns prefixed with "re:" are compiled as regex; everything else uses strings.Contains.
type RawPatterns struct {
	BusyPatterns   []string // plain strings + "re:" prefixed regex
	PromptPatterns []string
	SpinnerChars   []string
	WhimsicalWords []string
}

// ResolvedPatterns holds the compiled, ready-to-use patterns for status detection.
type ResolvedPatterns struct {
	BusyStrings   []string
	BusyRegexps   []*regexp.Regexp
	PromptStrings []string
	PromptRegexps []*regexp.Regexp
	SpinnerChars  []string

	// Pre-built combo patterns (from WhimsicalWords + SpinnerChars)
	ThinkingPattern         *regexp.Regexp
	ThinkingPatternEllipsis *regexp.Regexp
	SpinnerActivePattern    *regexp.Regexp
}

// DefaultRawPatterns returns the built-in detection patterns for a known tool.
// Returns nil for unknown tools (they have no defaults).
func DefaultRawPatterns(toolName string) *RawPatterns {
	switch strings.ToLower(toolName) {
	case "claude":
		return &RawPatterns{
			BusyPatterns: []string{
				`re:(?m)^[✳✽✶✻✢·]\s*.+…`, // PRIMARY: spinner + ellipsis (Claude 2.1.25+) — anchored to line start to prevent mid-line · in welcome banner from false-positiving
				"ctrl+c to interrupt", // SECONDARY: explicit busy text (current Claude)
				"esc to interrupt",    // FALLBACK: older Claude Code versions
			},
			SpinnerChars:   defaultSpinnerChars(),
			WhimsicalWords: defaultWhimsicalWords(),
		}
	case "gemini":
		return &RawPatterns{
			BusyPatterns:   []string{"esc to cancel"},
			PromptPatterns: []string{"gemini>", "Type your message"},
		}
	case "opencode":
		return &RawPatterns{
			BusyPatterns:   []string{"esc interrupt"},
			PromptPatterns: []string{"Ask anything"},
		}
	case "codex":
		return &RawPatterns{
			BusyPatterns: []string{
				"ctrl+c to interrupt",
				"esc to interrupt",
				"press esc to interrupt",
			},
			PromptPatterns: []string{"How can I help", "codex>", "Continue?"},
		}
	case "shell":
		return &RawPatterns{
			PromptPatterns: []string{"$ ", "# ", "% "},
		}
	default:
		return nil
	}
}

// defaultSpinnerChars returns the braille + asterisk spinner characters used by Claude Code.
func defaultSpinnerChars() []string {
	return []string{
		"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏",
		"✳", "✽", "✶", "✢", // Claude Code 2.1.25+ asterisk spinner (excl ✻ and · which appear in done/other states)
	}
}

// defaultWhimsicalWords returns all 90+ whimsical "thinking" words used by Claude Code.
func defaultWhimsicalWords() []string {
	return []string{
		"accomplishing", "actioning", "actualizing", "baking", "booping",
		"brewing", "calculating", "cerebrating", "channelling", "churning",
		"clauding", "coalescing", "cogitating", "combobulating", "computing",
		"concocting", "conjuring", "considering", "contemplating", "cooking",
		"crafting", "creating", "crunching", "deciphering", "deliberating",
		"determining", "discombobulating", "divining", "doing", "effecting",
		"elucidating", "enchanting", "envisioning", "finagling", "flibbertigibbeting",
		"forging", "forming", "frolicking", "generating", "germinating",
		"hatching", "herding", "honking", "hustling", "ideating",
		"imagining", "incubating", "inferring", "jiving", "manifesting",
		"marinating", "meandering", "moseying", "mulling", "mustering",
		"musing", "noodling", "percolating", "perusing", "philosophising",
		"pondering", "pontificating", "processing", "puttering", "puzzling",
		"reticulating", "ruminating", "scheming", "schlepping", "shimmying",
		"shucking", "simmering", "smooshing", "spelunking", "spinning",
		"stewing", "sussing", "synthesizing", "thinking", "tinkering",
		"transmuting", "unfurling", "unravelling", "vibing", "wandering",
		"whirring", "wibbling", "wizarding", "working", "wrangling",
		// Claude Code 2.1.25+ additions
		"billowing", "gusting", "metamorphosing", "sublimating", "recombobulating", "sautéing",
	}
}

// spinnerRuneSet returns the full set of spinner runes for content normalization.
// Includes both the "active-only" chars (used for busy detection) and the
// additional chars (·, ✻) that appear in done/other states but still need stripping
// for stable hashing.
func SpinnerRuneSet() []rune {
	return []rune{
		'⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏',
		'·', '✳', '✽', '✶', '✻', '✢', // all spinner-like chars for normalization
	}
}

// CompilePatterns compiles raw string patterns into ready-to-use ResolvedPatterns.
// Patterns prefixed with "re:" are compiled as regex. Invalid regex patterns are
// logged as warnings and skipped (never crash).
func CompilePatterns(raw *RawPatterns) (*ResolvedPatterns, error) {
	if raw == nil {
		return nil, fmt.Errorf("nil RawPatterns")
	}

	resolved := &ResolvedPatterns{}

	// Split busy patterns into strings vs regex
	for _, p := range raw.BusyPatterns {
		if strings.HasPrefix(p, "re:") {
			re, err := regexp.Compile(p[3:])
			if err != nil {
				patternLog.Warn("invalid_busy_regex",
					slog.String("pattern", p),
					slog.String("error", err.Error()))
				continue
			}
			resolved.BusyRegexps = append(resolved.BusyRegexps, re)
		} else {
			resolved.BusyStrings = append(resolved.BusyStrings, p)
		}
	}

	// Split prompt patterns into strings vs regex
	for _, p := range raw.PromptPatterns {
		if strings.HasPrefix(p, "re:") {
			re, err := regexp.Compile(p[3:])
			if err != nil {
				patternLog.Warn("invalid_prompt_regex",
					slog.String("pattern", p),
					slog.String("error", err.Error()))
				continue
			}
			resolved.PromptRegexps = append(resolved.PromptRegexps, re)
		} else {
			resolved.PromptStrings = append(resolved.PromptStrings, p)
		}
	}

	// Copy spinner chars
	resolved.SpinnerChars = make([]string, len(raw.SpinnerChars))
	copy(resolved.SpinnerChars, raw.SpinnerChars)

	// Build combo regex patterns from WhimsicalWords + SpinnerChars
	if len(raw.WhimsicalWords) > 0 && len(raw.SpinnerChars) > 0 {
		spinnerCharClass := buildSpinnerCharClass(raw.SpinnerChars)
		wordsAlt := strings.Join(raw.WhimsicalWords, "|")

		// ThinkingPattern: spinner + whimsical word + timing info
		tp, err := regexp.Compile(spinnerCharClass + `\s*(?i)(` + wordsAlt + `)[^(]*\([^)]*\)`)
		if err != nil {
			patternLog.Warn("failed_compile_thinking_pattern", slog.String("error", err.Error()))
		} else {
			resolved.ThinkingPattern = tp
		}

		// ThinkingPatternEllipsis: spinner + any text + unicode ellipsis + parens
		tpe, err := regexp.Compile(spinnerCharClass + `\s*.+…\s*\([^)]*\)`)
		if err != nil {
			patternLog.Warn("failed_compile_thinking_ellipsis_pattern", slog.String("error", err.Error()))
		} else {
			resolved.ThinkingPatternEllipsis = tpe
		}

		// SpinnerActivePattern: spinner + any text + unicode ellipsis
		sap, err := regexp.Compile(spinnerCharClass + `\s*.+…`)
		if err != nil {
			patternLog.Warn("failed_compile_spinner_active_pattern", slog.String("error", err.Error()))
		} else {
			resolved.SpinnerActivePattern = sap
		}
	}

	return resolved, nil
}

// buildSpinnerCharClass builds a regex character class from spinner char strings.
// e.g., ["⠋", "⠙", "✳"] -> "[⠋⠙✳]"
func buildSpinnerCharClass(chars []string) string {
	var b strings.Builder
	b.WriteRune('[')
	for _, ch := range chars {
		b.WriteString(regexp.QuoteMeta(ch))
	}
	b.WriteRune(']')
	return b.String()
}

// MergeRawPatterns merges defaults with overrides and extras.
//   - If overrides has a field set (non-nil slice, even if empty), it replaces the default.
//   - extras fields are appended to the result (after defaults or overrides).
//   - If defaults is nil, only overrides/extras are used.
func MergeRawPatterns(defaults, overrides, extras *RawPatterns) *RawPatterns {
	result := &RawPatterns{}

	// Start with defaults
	if defaults != nil {
		result.BusyPatterns = copySlice(defaults.BusyPatterns)
		result.PromptPatterns = copySlice(defaults.PromptPatterns)
		result.SpinnerChars = copySlice(defaults.SpinnerChars)
		result.WhimsicalWords = copySlice(defaults.WhimsicalWords)
	}

	// Apply overrides (replace entire field if set)
	if overrides != nil {
		if overrides.BusyPatterns != nil {
			result.BusyPatterns = copySlice(overrides.BusyPatterns)
		}
		if overrides.PromptPatterns != nil {
			result.PromptPatterns = copySlice(overrides.PromptPatterns)
		}
		if overrides.SpinnerChars != nil {
			result.SpinnerChars = copySlice(overrides.SpinnerChars)
		}
		if overrides.WhimsicalWords != nil {
			result.WhimsicalWords = copySlice(overrides.WhimsicalWords)
		}
	}

	// Append extras
	if extras != nil {
		result.BusyPatterns = append(result.BusyPatterns, extras.BusyPatterns...)
		result.PromptPatterns = append(result.PromptPatterns, extras.PromptPatterns...)
		result.SpinnerChars = append(result.SpinnerChars, extras.SpinnerChars...)
		result.WhimsicalWords = append(result.WhimsicalWords, extras.WhimsicalWords...)
	}

	return result
}

// spinnerRuneMap is a lookup table for O(1) spinner rune detection.
// Built once at init from SpinnerRuneSet() for use in single-pass stripping.
var spinnerRuneMap map[rune]bool

func init() {
	spinnerRuneMap = make(map[rune]bool, len(SpinnerRuneSet()))
	for _, r := range SpinnerRuneSet() {
		spinnerRuneMap[r] = true
	}
}

// StripSpinnerRunes removes all spinner characters in a single O(n) pass
// using strings.Map, replacing 16 sequential strings.ReplaceAll calls.
func StripSpinnerRunes(s string) string {
	return strings.Map(func(r rune) rune {
		if spinnerRuneMap[r] {
			return -1 // drop the rune
		}
		return r
	}, s)
}

func copySlice(s []string) []string {
	if s == nil {
		return nil
	}
	c := make([]string, len(s))
	copy(c, s)
	return c
}
