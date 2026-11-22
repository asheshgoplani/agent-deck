package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/voicedesk"
)

// `agent-deck voice home --json`: the session "Jarvis" talks to by default
// and "go home" returns to. One setting for the voice daemon, the phone and
// the Mac app: [voice] home (a session id or title), else (also when that
// session is gone) his conductor by the [voice] owner rules, else none. The daemon keeps its own "go home" words;
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
	// Error is set when [voice] home names no live session ("not found",
	// "ambiguous"); home is then the default conductor, if any.
	Error string `json:"error,omitempty"`
}

// voiceHomeRow is the part of a `list --json` row voice home reads.
type voiceHomeRow struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	ConductorName string `json:"conductor_name"`
	Group         string `json:"group"`
	Profile       string `json:"profile"`
	Archived      bool   `json:"archived"`
}

// voiceHomeList reads the profile's sessions (this binary's own read-only
// `list --json`). A variable so tests can count and fake it.
var voiceHomeList = func(ctx context.Context, profile string) ([]voiceHomeRow, error) {
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	d := &cliVoiceDeck{self: self, profile: profile}
	out, err := d.run(ctx, "", "-p", profile, "list", "--json")
	if err != nil {
		return nil, err
	}
	var rows []voiceHomeRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// resolveVoiceHome is resolveVoiceHomeFor with no owner rules (a
// one-person install: every session is his).
func resolveVoiceHome(configured, profile string, rows []voiceHomeRow) VoiceHome {
	return resolveVoiceHomeFor(voicedesk.Config{}, configured, profile, rows)
}

// resolveVoiceHomeFor picks home from the configured value and the
// profile's sessions: an exact id, an exact title (case-insensitive), a
// unique id prefix of 6 or more. It never guesses between two titles. With
// nothing configured, or when the configured session is gone (archived,
// renamed, missing) or ambiguous, home is his conductor (see
// defaultVoiceHome); in the second case Error keeps why the configured one
// was not used.
func resolveVoiceHomeFor(cfg voicedesk.Config, configured, profile string, rows []voiceHomeRow) VoiceHome {
	live := rows[:0:0]
	for _, r := range rows {
		if !r.Archived {
			live = append(live, r)
		}
	}
	want := strings.TrimSpace(configured)
	if want == "" {
		return defaultVoiceHome(cfg, profile, live)
	}
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
	h := defaultVoiceHome(cfg, profile, live)
	h.Error = "not found"
	if len(hits) > 1 {
		h.Error = "ambiguous"
	}
	return h
}

// defaultVoiceHome is his live conductor by the [voice] owner rules (never
// someone else's or nobody's): the one named like the profile, else one
// whose name ends in "-<profile>" (deck-<profile>, conductor-<profile>),
// else the first of his by name.
func defaultVoiceHome(cfg voicedesk.Config, profile string, live []voiceHomeRow) VoiceHome {
	var mine []voiceHomeRow
	for _, r := range live {
		if r.ConductorName == "" {
			continue
		}
		p := r.Profile
		if p == "" {
			p = profile
		}
		if cfg.Mine(voicedesk.Session{ID: r.ID, Title: r.Title, Group: r.Group, Profile: p}) {
			mine = append(mine, r)
		}
	}
	if len(mine) == 0 {
		return VoiceHome{Source: "none"}
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].ConductorName < mine[j].ConductorName })
	rank := func(c voiceHomeRow) int {
		n, p := strings.ToLower(c.ConductorName), strings.ToLower(strings.TrimSpace(profile))
		switch {
		case p != "" && n == p:
			return 0
		case p != "" && strings.HasSuffix(n, "-"+p):
			return 1
		}
		return 2
	}
	pick := mine[0]
	for _, c := range mine {
		if rank(c) < rank(pick) {
			pick = c
		}
	}
	return VoiceHome{ID: pick.ID, Title: pick.Title, Source: "default"}
}

// voiceHomeInputs reads the profile and the [voice] settings. When they
// cannot be read, bad is the answer to give.
func voiceHomeInputs(profile string) (resolved, configured string, cfg voicedesk.Config, bad *VoiceHome) {
	resolved, err := session.ResolveProfileForStorageQuiet(profile)
	if err != nil {
		return "", "", cfg, &VoiceHome{Source: "none", Error: "profile"}
	}
	uc, err := session.LoadUserConfig()
	if err != nil {
		return "", "", cfg, &VoiceHome{Source: "none", Error: "config"}
	}
	if uc != nil {
		configured = uc.Voice.Home
		cfg = voiceDeskConfig(uc.Voice)
	}
	return resolved, configured, cfg, nil
}

// currentVoiceHome resolves home now (one `list --json`) and refreshes the
// cache `voice state` reads.
func currentVoiceHome(profile string) VoiceHome {
	resolved, configured, cfg, bad := voiceHomeInputs(profile)
	if bad != nil {
		return *bad
	}
	return freshVoiceHome(resolved, configured, cfg)
}

func freshVoiceHome(resolved, configured string, cfg voicedesk.Config) VoiceHome {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rows, err := voiceHomeList(ctx, resolved)
	h := resolveVoiceHomeFor(cfg, configured, resolved, rows)
	if err == nil {
		writeVoiceHomeCache(resolved, voiceHomeCacheKey(configured, cfg), h)
	}
	return h
}

// voiceHomeCacheTTL bounds how long `voice state` reuses a resolved home.
// `voice state` is polled; listing sessions on every poll costs ~100 ms.
// A config change is seen at once (it is part of the key); a session
// archived or renamed is seen within the TTL.
const voiceHomeCacheTTL = 30 * time.Second

type voiceHomeCache struct {
	At   time.Time `json:"at"`
	Key  string    `json:"key"`
	Home VoiceHome `json:"home"`
}

func voiceHomeCacheKey(configured string, cfg voicedesk.Config) string {
	b, _ := json.Marshal(struct {
		Home string
		Cfg  voicedesk.Config
	}{configured, cfg})
	return string(b)
}

func voiceHomeCachePath(profile string) string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		return ""
	}
	name := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == os.PathSeparator || r < ' ' {
			return '_'
		}
		return r
	}, profile)
	return filepath.Join(dir, "agent-deck", "voice-home-"+name+".json")
}

func readVoiceHomeCache(profile, key string, now time.Time) (VoiceHome, bool) {
	p := voiceHomeCachePath(profile)
	if p == "" {
		return VoiceHome{}, false
	}
	b, err := os.ReadFile(p) //nolint:gosec // G304: our own cache file under the user cache dir
	if err != nil {
		return VoiceHome{}, false
	}
	var c voiceHomeCache
	if json.Unmarshal(b, &c) != nil || c.Key != key {
		return VoiceHome{}, false
	}
	if age := now.Sub(c.At); age < 0 || age > voiceHomeCacheTTL {
		return VoiceHome{}, false
	}
	return c.Home, true
}

func writeVoiceHomeCache(profile, key string, h VoiceHome) {
	p := voiceHomeCachePath(profile)
	if p == "" {
		return
	}
	b, err := json.Marshal(voiceHomeCache{At: time.Now(), Key: key, Home: h})
	if err != nil || os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".voice-home-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), p) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// withVoiceHome adds "home" to the voice state object, from the cache when
// it is fresh (no session list on every poll).
func withVoiceHome(state []byte, profile string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(state, &m); err != nil {
		return nil, err
	}
	resolved, configured, cfg, bad := voiceHomeInputs(profile)
	switch {
	case bad != nil:
		m["home"] = *bad
	default:
		if h, ok := readVoiceHomeCache(resolved, voiceHomeCacheKey(configured, cfg), time.Now()); ok {
			m["home"] = h
		} else {
			m["home"] = freshVoiceHome(resolved, configured, cfg)
		}
	}
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
	case h.Error != "":
		fmt.Fprintf(stdout, "voice home: %s (%s; [voice] home: %s)\n", h.Title, h.Source, h.Error)
	default:
		fmt.Fprintf(stdout, "voice home: %s (%s)\n", h.Title, h.Source)
	}
	return 0
}
