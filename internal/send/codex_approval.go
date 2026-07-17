package send

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// CodexApprovalTarget is the minimum tmux surface needed to safely resolve a
// Codex approval overlay. SendNamedKey emits one tmux key event without an
// implicit Enter or message/paste semantics.
type CodexApprovalTarget interface {
	CapturePaneFresh() (string, error)
	SendNamedKey(string) error
}

// CodexApprovalOptions controls the bounded post-key verification.
type CodexApprovalOptions struct {
	VerifyTimeout time.Duration
	PollInterval  time.Duration
}

// CodexApprovalResult describes the option selected and whether the original
// approval overlay was observed disappearing.
type CodexApprovalResult struct {
	Choice         string
	OptionNumber   int
	OptionLabel    string
	KeySent        bool
	Verified       bool
	NextPromptSeen bool
}

type codexApprovalOption struct {
	number int
	label  string
}

type codexApprovalPrompt struct {
	fingerprint string
	options     []codexApprovalOption
}

var codexApprovalOptionPattern = regexp.MustCompile(`^\s*(›\s*)?([1-9][0-9]*)\.\s+(.+?)\s*$`)

// ApproveCodexPrompt resolves one currently visible Codex approval menu.
//
// choice accepts a displayed option number, or one of "once", "always", and
// "session". The displayed number is sent as one literal keypress; Enter is
// intentionally not sent because Codex selects numbered approval options on
// the digit KeyEvent itself.
func ApproveCodexPrompt(target CodexApprovalTarget, choice string, opts CodexApprovalOptions) (CodexApprovalResult, error) {
	var result CodexApprovalResult
	if target == nil {
		return result, fmt.Errorf("approval target is nil")
	}
	if opts.VerifyTimeout <= 0 {
		opts.VerifyTimeout = 5 * time.Second
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = 50 * time.Millisecond
	}

	first, err := captureCodexApprovalPrompt(target)
	if err != nil {
		return result, err
	}
	if first == nil {
		return result, fmt.Errorf("no active Codex approval prompt found")
	}

	selected, normalizedChoice, err := selectCodexApprovalOption(first, choice)
	if err != nil {
		return result, err
	}
	result.Choice = normalizedChoice
	result.OptionNumber = selected.number
	result.OptionLabel = selected.label

	// Re-read immediately before dispatch. This closes the widest part of the
	// capture→send race and, importantly, fails closed rather than typing a
	// digit into Codex's normal composer when the overlay has changed.
	second, err := captureCodexApprovalPrompt(target)
	if err != nil {
		return result, err
	}
	if second == nil || second.fingerprint != first.fingerprint {
		return result, fmt.Errorf("Codex approval prompt changed before the key could be sent")
	}
	if _, _, err := selectCodexApprovalOption(second, strconv.Itoa(selected.number)); err != nil {
		return result, fmt.Errorf("Codex approval option changed before dispatch: %w", err)
	}

	if err := target.SendNamedKey(strconv.Itoa(selected.number)); err != nil {
		return result, fmt.Errorf("send Codex approval key: %w", err)
	}
	result.KeySent = true

	deadline := time.Now().Add(opts.VerifyTimeout)
	for {
		current, captureErr := captureCodexApprovalPrompt(target)
		if captureErr == nil {
			if current == nil {
				result.Verified = true
				return result, nil
			}
			if current.fingerprint != first.fingerprint {
				result.Verified = true
				result.NextPromptSeen = true
				return result, nil
			}
		}
		if !time.Now().Before(deadline) {
			return result, fmt.Errorf(
				"approval key %d was sent, but the original Codex prompt did not clear within %s; not retrying automatically",
				selected.number, opts.VerifyTimeout,
			)
		}
		time.Sleep(opts.PollInterval)
	}
}

func captureCodexApprovalPrompt(target CodexApprovalTarget) (*codexApprovalPrompt, error) {
	raw, err := target.CapturePaneFresh()
	if err != nil {
		return nil, fmt.Errorf("capture Codex pane: %w", err)
	}
	return detectCodexApprovalPrompt(tmux.StripANSI(raw)), nil
}

// detectCodexApprovalPrompt recognizes only a live numbered approval overlay.
// Requiring a selected "› N." row plus both affirmative and negative options
// avoids confusing the ordinary "›" composer or stale approval history for a
// current decision gate.
func detectCodexApprovalPrompt(content string) *codexApprovalPrompt {
	lines := strings.Split(content, "\n")
	start := len(lines) - 40
	if start < 0 {
		start = 0
	}

	selectedLine := -1
	for i := len(lines) - 1; i >= start; i-- {
		match := codexApprovalOptionPattern.FindStringSubmatch(lines[i])
		if match != nil && strings.TrimSpace(match[1]) != "" {
			selectedLine = i
			break
		}
	}
	if selectedLine < 0 {
		return nil
	}

	blockStart := selectedLine - 12
	if blockStart < start {
		blockStart = start
	}
	blockEnd := selectedLine + 16
	if blockEnd >= len(lines) {
		blockEnd = len(lines) - 1
	}

	var options []codexApprovalOption
	hasYes := false
	hasNo := false
	firstOptionLine := -1
	lastOptionLine := selectedLine
	for i := blockStart; i <= blockEnd; i++ {
		match := codexApprovalOptionPattern.FindStringSubmatch(lines[i])
		if match == nil {
			continue
		}
		number, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		label := strings.Join(strings.Fields(match[3]), " ")
		lower := strings.ToLower(label)
		hasYes = hasYes || strings.HasPrefix(lower, "yes")
		hasNo = hasNo || strings.HasPrefix(lower, "no")
		options = append(options, codexApprovalOption{number: number, label: label})
		if firstOptionLine < 0 {
			firstOptionLine = i
		}
		lastOptionLine = i
	}
	if len(options) < 2 || !hasYes || !hasNo {
		return nil
	}

	// Include the stable request context above the menu, not just its generic
	// option labels, so a queued second approval is recognized as a new prompt.
	contextStart := firstOptionLine - 12
	if contextStart < start {
		contextStart = start
	}
	var fingerprintLines []string
	for i := contextStart; i <= lastOptionLine; i++ {
		line := strings.TrimSpace(lines[i])
		line = strings.TrimSpace(strings.TrimPrefix(line, "›"))
		normalized := strings.Join(strings.Fields(line), " ")
		if normalized != "" {
			fingerprintLines = append(fingerprintLines, normalized)
		}
	}

	return &codexApprovalPrompt{
		fingerprint: strings.Join(fingerprintLines, "\n"),
		options:     options,
	}
}

func selectCodexApprovalOption(prompt *codexApprovalPrompt, choice string) (codexApprovalOption, string, error) {
	if prompt == nil {
		return codexApprovalOption{}, "", fmt.Errorf("no Codex approval prompt")
	}
	normalized := strings.ToLower(strings.TrimSpace(choice))
	if normalized == "" {
		normalized = "once"
	}

	if number, err := strconv.Atoi(normalized); err == nil {
		if number < 1 || number > 9 {
			return codexApprovalOption{}, normalized, fmt.Errorf(
				"Codex approval option %d cannot be sent as a single keypress", number,
			)
		}
		for _, option := range prompt.options {
			if option.number == number {
				return option, strconv.Itoa(number), nil
			}
		}
		return codexApprovalOption{}, normalized, fmt.Errorf("Codex approval option %d is not displayed", number)
	}

	for _, option := range prompt.options {
		label := strings.ToLower(option.label)
		switch normalized {
		case "once":
			if strings.HasPrefix(label, "yes") &&
				(strings.Contains(label, "proceed") || strings.Contains(label, "just this once")) {
				return option, normalized, nil
			}
		case "always", "prefix":
			if strings.HasPrefix(label, "yes") &&
				!strings.Contains(label, "this session") &&
				!strings.Contains(label, "this conversation") &&
				(strings.Contains(label, "don't ask again") || strings.Contains(label, "in the future")) {
				return option, "always", nil
			}
		case "session":
			if strings.HasPrefix(label, "yes") &&
				(strings.Contains(label, "this session") || strings.Contains(label, "this conversation")) {
				return option, normalized, nil
			}
		default:
			return codexApprovalOption{}, normalized, fmt.Errorf(
				"invalid approval choice %q (use once, always, session, or a displayed option number)",
				choice,
			)
		}
	}

	return codexApprovalOption{}, normalized, fmt.Errorf("Codex approval prompt does not offer choice %q", normalized)
}
