package costs

import (
	"regexp"
)

var openaiTokenRe = regexp.MustCompile(`Tokens used:\s*([\d,]+)\s*prompt\s*\+\s*([\d,]+)\s*completion\s*\(([^)]+)\)`)

// OpenAIOutputParser parses OpenAI/Codex CLI token usage output.
type OpenAIOutputParser struct {
	pricer *Pricer
}

func (p *OpenAIOutputParser) Name() string { return "openai" }

func (p *OpenAIOutputParser) CanParse(toolType string) bool {
	return toolType == "codex" || toolType == "openai"
}

func (p *OpenAIOutputParser) Parse(input string) ([]CostEvent, error) {
	m := openaiTokenRe.FindStringSubmatch(input)
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
