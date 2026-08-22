package oracle

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Reading is one probe's outcome: the number, and the literal text it came
// from.
//
// The evidence is not decoration. A parity row that says "27000" is a claim; a
// row that says "27000, read from `27.0k / 1.0M  (2.7%)`" is a fact a reviewer
// can check against the frame without trusting this code. Every failure mode
// this framework exists to catch was a number nobody traced back to a screen.
type Reading struct {
	// Found reports whether the probe matched anything at all.
	Found bool `json:"found"`
	// Value is the extracted number.
	Value float64 `json:"value"`
	// Raw is the captured text, before humanizing.
	Raw string `json:"raw,omitempty"`
	// Evidence is the line the capture came from, trimmed.
	Evidence string `json:"evidence,omitempty"`
	// Line is the 1-based line the evidence came from, 0 for JSON.
	Line int `json:"line,omitempty"`
	// Parts lists every capture when the reducer combined more than one, so a
	// sum shows its terms rather than only its answer.
	Parts []string `json:"parts,omitempty"`
	// Error explains a probe that could not be evaluated.
	Error string `json:"error,omitempty"`
}

// ansiRe strips terminal escape sequences so a probe reads what a person read.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// StripANSI removes escape sequences from a captured frame.
func StripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// Read applies a probe to a document.
//
// isJSON selects the addressing mode: a selector walks the parsed document, a
// pattern list scans the text line by line. A probe declaring the wrong one for
// its source is a validation error at the call site, not a silent zero here —
// a comparator that returns 0 when it cannot find a number will one day report
// that two systems agree that nothing is there.
func Read(p Probe, doc string, isJSON bool) Reading {
	if strings.TrimSpace(p.Select) != "" {
		if !isJSON {
			return Reading{Error: "this probe addresses JSON, but its source is text"}
		}
		return readJSON(p, doc)
	}
	if isJSON {
		// Patterns over a JSON document are legitimate — it is still text — but
		// say so, so a reader of parity.json knows the selector was not used.
		return readText(p, doc)
	}
	return readText(p, doc)
}

// readText scans the document for each pattern's single capture group and
// combines the results.
func readText(p Probe, doc string) Reading {
	lines := strings.Split(StripANSI(strings.ReplaceAll(doc, "\r\n", "\n")), "\n")
	var (
		vals     []float64
		raws     []string
		evidence string
		lineNo   int
	)
	for _, pat := range p.Patterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			return Reading{Error: "pattern does not compile: " + err.Error()}
		}
		for i, ln := range lines {
			m := re.FindStringSubmatch(ln)
			if m == nil {
				continue
			}
			v, err := parseNumber(m[1], p.Humanize)
			if err != nil {
				return Reading{Error: fmt.Sprintf("line %d: %v", i+1, err), Evidence: strings.TrimSpace(ln), Line: i + 1}
			}
			vals = append(vals, v)
			raws = append(raws, m[1])
			if evidence == "" {
				evidence, lineNo = strings.TrimSpace(ln), i+1
			}
			// One match per pattern. A pattern that should collect several
			// occurrences is several patterns, or a reducer over them; letting
			// one pattern silently harvest a whole screen is how a sum picks up
			// a number from a caveat three panels down.
			break
		}
	}
	if len(vals) == 0 {
		return Reading{}
	}
	out := Reading{Found: true, Raw: strings.Join(raws, " + "), Evidence: evidence, Line: lineNo}
	if len(vals) > 1 {
		out.Parts = raws
	}
	out.Value = reduce(p.Reduce, vals) * scaleOf(p)
	return out
}

// readJSON walks the document with a selector.
func readJSON(p Probe, doc string) Reading {
	var root any
	if err := json.Unmarshal([]byte(doc), &root); err != nil {
		return Reading{Error: "source is not readable JSON: " + err.Error()}
	}
	node, err := selectPath(root, p.Select)
	if err != nil {
		return Reading{Error: err.Error()}
	}
	if node == nil {
		return Reading{}
	}
	switch v := node.(type) {
	case float64:
		return Reading{Found: true, Value: v * scaleOf(p), Raw: strconv.FormatFloat(v, 'f', -1, 64), Evidence: p.Select}
	case string:
		n, err := parseNumber(v, p.Humanize)
		if err != nil {
			return Reading{Error: fmt.Sprintf("%s holds %q: %v", p.Select, v, err)}
		}
		return Reading{Found: true, Value: n * scaleOf(p), Raw: v, Evidence: p.Select}
	default:
		return Reading{Error: fmt.Sprintf("%s does not address a number (got %T)", p.Select, node)}
	}
}

// selectorStep matches one addressing step: a key, an index, or a match filter.
var selectorStep = regexp.MustCompile(`^([^.\[\]]*)((?:\[[^\]]*\])*)$`)

var indexOrFilter = regexp.MustCompile(`\[([^\]]*)\]`)

// selectPath walks a decoded JSON document.
//
// The supported forms are deliberately few, because a selector language is a
// place bugs hide: "$.a.b" for objects, "[0]" for arrays, and "[key=value]" to
// pick the array element whose field equals a string. Anything more expressive
// belongs in the declaration as a regexp over the text, where the reader can
// see exactly what was matched.
func selectPath(root any, sel string) (any, error) {
	s := strings.TrimPrefix(strings.TrimSpace(sel), "$")
	s = strings.TrimPrefix(s, ".")
	node := root
	if s == "" {
		return node, nil
	}
	for _, raw := range strings.Split(s, ".") {
		m := selectorStep.FindStringSubmatch(raw)
		if m == nil {
			return nil, fmt.Errorf("selector %q: cannot parse step %q", sel, raw)
		}
		if key := m[1]; key != "" {
			obj, ok := node.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("selector %q: %q is not an object", sel, key)
			}
			v, present := obj[key]
			if !present {
				return nil, nil
			}
			node = v
		}
		for _, sub := range indexOrFilter.FindAllStringSubmatch(m[2], -1) {
			arr, ok := node.([]any)
			if !ok {
				return nil, fmt.Errorf("selector %q: %q is not an array", sel, raw)
			}
			expr := strings.TrimSpace(sub[1])
			if idx, err := strconv.Atoi(expr); err == nil {
				if idx < 0 || idx >= len(arr) {
					return nil, nil
				}
				node = arr[idx]
				continue
			}
			field, want, found := strings.Cut(expr, "=")
			if !found {
				return nil, fmt.Errorf("selector %q: %q is neither an index nor a field=value filter", sel, expr)
			}
			var hit any
			for _, el := range arr {
				obj, ok := el.(map[string]any)
				if !ok {
					continue
				}
				if fmt.Sprint(obj[strings.TrimSpace(field)]) == strings.TrimSpace(want) {
					hit = el
					break
				}
			}
			if hit == nil {
				return nil, nil
			}
			node = hit
		}
	}
	return node, nil
}

func scaleOf(p Probe) float64 {
	if p.Scale == 0 {
		return 1
	}
	return p.Scale
}

func reduce(r Reduce, vals []float64) float64 {
	switch r {
	case ReduceSum:
		var sum float64
		for _, v := range vals {
			sum += v
		}
		return sum
	case ReduceLast:
		return vals[len(vals)-1]
	case ReduceMax:
		out := vals[0]
		for _, v := range vals[1:] {
			if v > out {
				out = v
			}
		}
		return out
	case ReduceMin:
		out := vals[0]
		for _, v := range vals[1:] {
			if v < out {
				out = v
			}
		}
		return out
	default:
		return vals[0]
	}
}

// humanSuffix are the magnitudes a screen prints.
var humanSuffix = map[string]float64{"k": 1e3, "K": 1e3, "m": 1e6, "M": 1e6, "g": 1e9, "G": 1e9}

// parseNumber reads a figure as it appears in a document.
//
// With humanize, it accepts what a terminal actually prints: thousands
// separators, a "k"/"M" magnitude, and a leading "~" or "≈" that marks the
// number as an estimate. The marker is stripped rather than rejected because
// this comparator's whole job is to notice when a number IS an estimate — it
// records that in the reading's raw text and grades the figure on its value.
func parseNumber(s string, humanize bool) (float64, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, fmt.Errorf("captured an empty string where a number was expected")
	}
	if humanize {
		t = strings.TrimLeft(t, "~≈+")
		t = strings.ReplaceAll(t, ",", "")
		t = strings.ReplaceAll(t, " ", "")
		if len(t) > 1 {
			last := t[len(t)-1:]
			if mult, ok := humanSuffix[last]; ok {
				n, err := strconv.ParseFloat(strings.TrimSpace(t[:len(t)-1]), 64)
				if err != nil {
					return 0, fmt.Errorf("cannot read %q as a number: %w", s, err)
				}
				return n * mult, nil
			}
		}
	}
	n, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot read %q as a number: %w", s, err)
	}
	return n, nil
}
