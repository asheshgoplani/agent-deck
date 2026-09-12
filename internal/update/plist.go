package update

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// LaunchAgent is the subset of a launchd property list that the post-install
// hygiene needs: enough to decide whether the agent runs our binary and
// whether launchd is expected to keep it running.
type LaunchAgent struct {
	Path             string
	Label            string
	Program          string
	ProgramArguments []string
	KeepAlive        bool
	RunAtLoad        bool
}

// ProgramPath is the executable launchd starts: Program when set, otherwise
// ProgramArguments[0].
func (a LaunchAgent) ProgramPath() string {
	if a.Program != "" {
		return a.Program
	}
	if len(a.ProgramArguments) > 0 {
		return a.ProgramArguments[0]
	}
	return ""
}

// ParseLaunchAgentPlist reads the top-level dict of an XML plist. Only the
// keys LaunchAgent carries are interpreted; everything else is walked and
// ignored. The repo does not depend on a plist library, and the handful of
// scalar/array/dict node types launchd uses fit in a small encoding/xml
// walker.
func ParseLaunchAgentPlist(data []byte) (LaunchAgent, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	var agent LaunchAgent
	root, err := plistRootDict(dec)
	if err != nil {
		return agent, err
	}
	if s, ok := root["Label"].(string); ok {
		agent.Label = s
	}
	if s, ok := root["Program"].(string); ok {
		agent.Program = s
	}
	if arr, ok := root["ProgramArguments"].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				agent.ProgramArguments = append(agent.ProgramArguments, s)
			}
		}
	}
	agent.KeepAlive = plistTruthy(root["KeepAlive"])
	agent.RunAtLoad = plistTruthy(root["RunAtLoad"])
	if agent.Label == "" {
		return agent, errors.New("plist has no Label")
	}
	return agent, nil
}

// plistTruthy treats <true/> as true and a KeepAlive <dict> (launchd's
// conditional keep-alive form, e.g. SuccessfulExit) as "launchd may restart
// it", which for our verify step is the conservative reading.
func plistTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case map[string]any:
		return len(t) > 0
	}
	return false
}

// plistRootDict advances to <plist><dict> and decodes that dict.
func plistRootDict(dec *xml.Decoder) (map[string]any, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return nil, errors.New("plist has no top-level dict")
			}
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "plist":
			continue
		case "dict":
			return plistDecodeDict(dec)
		default:
			return nil, fmt.Errorf("unexpected top-level <%s>", se.Name.Local)
		}
	}
}

// plistDecodeDict consumes tokens up to the matching </dict>.
func plistDecodeDict(dec *xml.Decoder) (map[string]any, error) {
	out := map[string]any{}
	var key string
	haveKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			if t.Name.Local == "dict" {
				return out, nil
			}
		case xml.StartElement:
			if t.Name.Local == "key" {
				var k string
				if err := dec.DecodeElement(&k, &t); err != nil {
					return nil, err
				}
				key, haveKey = k, true
				continue
			}
			val, err := plistDecodeValue(dec, t)
			if err != nil {
				return nil, err
			}
			if haveKey {
				out[key] = val
				haveKey = false
			}
		}
	}
}

// plistDecodeValue decodes one value element whose start tag was just read.
func plistDecodeValue(dec *xml.Decoder, se xml.StartElement) (any, error) {
	switch se.Name.Local {
	case "string", "date", "data":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		return s, nil
	case "integer":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad <integer> %q", s)
		}
		return n, nil
	case "real":
		var s string
		if err := dec.DecodeElement(&s, &se); err != nil {
			return nil, err
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, fmt.Errorf("bad <real> %q", s)
		}
		return f, nil
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, err
		}
		return se.Name.Local == "true", nil
	case "dict":
		return plistDecodeDict(dec)
	case "array":
		var arr []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			switch t := tok.(type) {
			case xml.EndElement:
				if t.Name.Local == "array" {
					return arr, nil
				}
			case xml.StartElement:
				v, err := plistDecodeValue(dec, t)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported plist element <%s>", se.Name.Local)
	}
}
