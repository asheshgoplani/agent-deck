package session

import (
	"os"
	"regexp"
)

// extractFuncBodyInstance returns the body of a Go function declared in
// internal/session/instance.go. Brace-counting is intentionally simple —
// sufficient for the tightly-scoped structural checks in this package.
func extractFuncBodyInstance(fnName string) string {
	data, err := os.ReadFile("instance.go")
	if err != nil {
		return ""
	}
	src := string(data)
	re := regexp.MustCompile(`(?m)^func[^\n]*\b` + regexp.QuoteMeta(fnName) + `\s*\(`)
	loc := re.FindStringIndex(src)
	if loc == nil {
		return ""
	}
	i := loc[1]
	for i < len(src) && src[i] != '{' {
		i++
	}
	if i == len(src) {
		return ""
	}
	start := i + 1
	depth := 1
	for j := start; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start:j]
			}
		}
	}
	return ""
}
