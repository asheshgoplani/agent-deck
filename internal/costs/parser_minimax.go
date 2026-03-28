package costs

import (
	"regexp"
)

// minimaxTokenRe matches MiniMax CLI output in the format:
//
//	MiniMax usage: 1,234 input tokens, 567 output tokens (MiniMax-M2.7)
var minimaxTokenRe = regexp.MustCompile(
	`MiniMax usage:\s*([\d,]+)\s*input tokens?,\s*([\d,]+)\s*output tokens?\s*\(([^)]+)\)`,
)

// MiniMaxOutputParser parses MiniMax CLI/tool token usage output.
type MiniMaxOutputParser struct {
	pricer *Pricer
}

func (p *MiniMaxOutputParser) Name() string { return "minimax" }

func (p *MiniMaxOutputParser) CanParse(toolType string) bool {
	return toolType == "minimax"
}

func (p *MiniMaxOutputParser) Parse(input string) ([]CostEvent, error) {
	m := minimaxTokenRe.FindStringSubmatch(input)
	if m == nil {
		return nil, nil
	}
	inputTokens := parseCommaInt(m[1])
	outputTokens := parseCommaInt(m[2])
	model := m[3]
	ev := CostEvent{
		Model:        model,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
	return []CostEvent{ev}, nil
}
