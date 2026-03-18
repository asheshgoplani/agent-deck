package costs

import (
	"regexp"
	"strconv"
	"strings"
)

var geminiTokenRe = regexp.MustCompile(`Token count:\s*([\d,]+)\s*input,\s*([\d,]+)\s*output`)

// GeminiOutputParser parses Gemini CLI token usage output.
type GeminiOutputParser struct {
	pricer *Pricer
}

func (p *GeminiOutputParser) Name() string { return "gemini" }

func (p *GeminiOutputParser) CanParse(toolType string) bool {
	return toolType == "gemini"
}

func (p *GeminiOutputParser) Parse(input string) ([]CostEvent, error) {
	m := geminiTokenRe.FindStringSubmatch(input)
	if m == nil {
		return nil, nil
	}
	inputTokens := parseCommaInt(m[1])
	outputTokens := parseCommaInt(m[2])
	ev := CostEvent{
		Model:        "gemini-2.5-pro",
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
	return []CostEvent{ev}, nil
}

// parseCommaInt parses a comma-separated integer string like "1,234" into 1234.
func parseCommaInt(s string) int64 {
	s = strings.ReplaceAll(s, ",", "")
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
