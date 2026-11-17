package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `agent-deck voice home --json`: the session "Jarvis" talks to by default
// and "go home" returns to. One setting for the voice daemon, the phone and
// the Mac app: [voice] home (a session id or title), else the profile's
// conductor session, else none. The daemon keeps its own "go home" words;
// this only says where home is.

func init() {
	voiceSubs = append(voiceSubs, voiceSub{
		name:    "home",
		summary: "The voice home session: [voice] home, else the conductor [--json]",
		usage:   "  agent-deck voice home --json   {id, title, host, source: config|default|none}",
		run:     runVoiceHome,
	})
}

// VoiceHome is the result of `voice home --json` and the "home" field of
// `voice state --json`.
type VoiceHome struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Host   string `json:"host"`
	Source string `json:"source"` // config | default | none
	// Error is set when [voice] home names no session ("not found").
	Error string `json:"error,omitempty"`
}

// voiceHomeRow is the part of a `list --json` row voice home reads.
type voiceHomeRow struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	ConductorName string `json:"conductor_name"`
	Archived      bool   `json:"archived"`
}

// resolveVoiceHome picks home from the configured value and the profile's
// sessions: an exact id, an exact title (case-insensitive), a unique id
// prefix of 6 or more; with nothing configured the conductor session (the
// one named like the profile when there are several, else the first by
// name). It never guesses between two titles.
func resolveVoiceHome(configured, profile string, rows []voiceHomeRow) VoiceHome {
	live := rows[:0:0]
	for _, r := range rows {
		if !r.Archived {
			live = append(live, r)
		}
	}
	want := strings.TrimSpace(configured)
	if want != "" {
		var hits []voiceHomeRow
		for _, r := range live {
			if r.ID == want {
				return VoiceHome{ID: r.ID, Title: r.Title, Source: "config"}
			}
			if strings.EqualFold(strings.TrimSpace(r.Title), want) {
				hits = append(hits, r)
			}
		}
		if len(hits) == 0 && len(want) >= 6 && !strings.ContainsAny(want, " \t") {
			for _, r := range live {
				if strings.HasPrefix(r.ID, want) {
					hits = append(hits, r)
				}
			}
		}
		if len(hits) == 1 {
			return VoiceHome{ID: hits[0].ID, Title: hits[0].Title, Source: "config"}
		}
		why := "not found"
		if len(hits) > 1 {
			why = "ambiguous"
		}
		return VoiceHome{Source: "none", Error: why}
	}
	var conductors []voiceHomeRow
	for _, r := range live {
		if r.ConductorName != "" {
			conductors = append(conductors, r)
		}
	}
	if len(conductors) == 0 {
		return VoiceHome{Source: "none"}
	}
	sort.SliceStable(conductors, func(i, j int) bool { return conductors[i].ConductorName < conductors[j].ConductorName })
	pick := conductors[0]
	for _, c := range conductors {
		if strings.EqualFold(c.ConductorName, profile) {
			pick = c
			break
		}
	}
	return VoiceHome{ID: pick.ID, Title: pick.Title, Source: "default"}
}

// currentVoiceHome reads the setting and the profile's sessions (this
// binary's own read-only `list --json`).
func currentVoiceHome(profile string) VoiceHome {
	resolved, err := session.ResolveProfileForStorageQuiet(profile)
	if err != nil {
		return VoiceHome{Source: "none", Error: "profile"}
	}
	configured := ""
	if uc, err := session.LoadUserConfig(); err == nil && uc != nil {
		configured = uc.Voice.Home
	} else if err != nil {
		return VoiceHome{Source: "none", Error: "config"}
	}
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d := &cliVoiceDeck{self: self, profile: resolved}
	out, err := d.run(ctx, "", "-p", resolved, "list", "--json")
	var rows []voiceHomeRow
	if err != nil || json.Unmarshal(out, &rows) != nil {
		rows = nil
	}
	return resolveVoiceHome(configured, resolved, rows)
}

// withVoiceHome adds "home" to the voice state object.
func withVoiceHome(state []byte, profile string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(state, &m); err != nil {
		return nil, err
	}
	m["home"] = currentVoiceHome(profile)
	return json.Marshal(m)
}

func runVoiceHome(profile string, args []string, stdout, stderr io.Writer) int {
	asJSON := false
	for _, a := range args {
		switch a {
		case "--json", "-json":
			asJSON = true
		case "--help", "-h":
			fmt.Fprintln(stdout, "Usage: agent-deck voice home [--json]")
			return 0
		default:
			fmt.Fprintf(stderr, "voice home: unknown argument %q\n\n", a)
			writeVoiceUsage(stderr)
			return 2
		}
	}
	h := currentVoiceHome(profile)
	if asJSON {
		b, err := json.Marshal(h)
		if err != nil {
			fmt.Fprintln(stderr, "voice home:", err)
			return 1
		}
		fmt.Fprintln(stdout, string(b))
		return 0
	}
	switch {
	case h.Source == "none" && h.Error != "":
		fmt.Fprintf(stdout, "voice home: none ([voice] home: %s)\n", h.Error)
	case h.Source == "none":
		fmt.Fprintln(stdout, "voice home: none")
	default:
		fmt.Fprintf(stdout, "voice home: %s (%s)\n", h.Title, h.Source)
	}
	return 0
}
