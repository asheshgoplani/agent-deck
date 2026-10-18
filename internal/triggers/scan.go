package triggers

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Scan caps: the scan runs on demand and from `triggers` at most once a day,
// never from a render path.
const (
	MaxScanFiles  = 500
	MaxScriptSize = 256 << 10
)

// Found is one unit the scan found that calls agent-deck.
type Found struct {
	Kind          string // launchd | systemd
	Unit          string // launchd label or systemd unit name without suffix
	ConfigPath    string
	Verb          string // send | start | restart | launch | ""
	Target        string // as written; "" when the scan could not read it
	TargetBy      string // title | id
	TargetProfile string
	Schedule      string
	// Source is the --source the unit passes ("" when none or unreadable). A
	// named kind (heartbeat:ops) becomes the row's id, so the scanned unit
	// and the sends it tags are one row.
	Source string
}

// ID is the trigger key: the unit's --source when it names a kind,
// otherwise "<kind>:<unit>" (which a bare --source name also maps to).
func (f Found) ID() string {
	if src := f.namedSource(); src != nil {
		return src.ID()
	}
	return f.Kind + ":" + f.Unit
}

// TriggerKind is the kind shown for the row.
func (f Found) TriggerKind() string {
	if src := f.namedSource(); src != nil {
		return src.Kind
	}
	return f.Kind
}

// Name is the row's display name.
func (f Found) Name() string {
	if src := f.namedSource(); src != nil {
		return src.Name
	}
	return f.Unit
}

func (f Found) namedSource() *Source {
	if f.Source == "" {
		return nil
	}
	src := ParseExplicit(f.Source)
	if src.Kind == "job" || src.Name == "" {
		return nil
	}
	return &src
}

// ScanDirs are the places a scan looks; tests point them at fixtures.
type ScanDirs struct {
	LaunchAgents string // ~/Library/LaunchAgents
	SystemdUser  string // ~/.config/systemd/user
}

// DefaultScanDirs returns the per-user unit directories under home.
func DefaultScanDirs(home string) ScanDirs {
	return ScanDirs{
		LaunchAgents: filepath.Join(home, "Library", "LaunchAgents"),
		SystemdUser:  filepath.Join(home, ".config", "systemd", "user"),
	}
}

// Scan reads both unit directories and returns every unit whose command, or
// the script it runs (followed one level, size-capped), mentions agent-deck.
func Scan(dirs ScanDirs) []Found {
	budget := MaxScanFiles
	var out []Found
	out = append(out, scanLaunchd(dirs.LaunchAgents, &budget)...)
	out = append(out, scanSystemd(dirs.SystemdUser, &budget)...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

func listDir(dir string, budget *int, suffixes ...string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if *budget <= 0 {
			break
		}
		name := e.Name()
		for _, suf := range suffixes {
			if strings.HasSuffix(name, suf) {
				*budget--
				out = append(out, filepath.Join(dir, name))
				break
			}
		}
	}
	return out
}

func readCapped(path string) (string, bool) {
	// Stat before opening: opening a FIFO named like a unit would block.
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxScriptSize))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// --- what the command does ---

var (
	// agent-deck as a command word (bare or at the end of a path), not as
	// part of a path (~/.agent-deck/...) or a name (agent-deck-autoupdate).
	agentDeckCall = regexp.MustCompile(`(?m)(?:^|[\s;&|("'/=])agent-deck(?:$|[\s"';|&)])`)
	// agent-deck [-p P] session send|start|restart <target> / launch
	verbPattern = regexp.MustCompile(`agent-deck(?:"|')?((?:\s+(?:-p|--profile)(?:\s+|=)(?:"[^"]*"|'[^']*'|\S+))*)\s+(?:session\s+(send|start|restart)|(launch))\b((?:\s+-{1,2}[A-Za-z-]+(?:=\S+)?)*)\s+("[^"]*"|'[^']*'|\S+)`)
	profileArg  = regexp.MustCompile(`(?:-p|--profile)(?:\s+|=)("[^"]*"|'[^']*'|\S+)`)
	sourceArg   = regexp.MustCompile(`--source(?:\s+|=)("[^"]*"|'[^']*'|\S+)`)
	assignment  = regexp.MustCompile(`(?m)^\s*(?:export\s+|readonly\s+|local\s+)?([A-Za-z_][A-Za-z0-9_]*)=("[^"\n]*"|'[^'\n]*'|[^\s;#]+)`)
	varRef      = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}?$`)
	sessionIDRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9]{6,}$|^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// describe fills verb, target and profile from a command line plus the
// script it runs. A target held in a shell variable is resolved from a plain
// assignment in the same script (VAR=x, VAR=${VAR:-x}); otherwise it stays
// unknown rather than guessed.
func describe(f *Found, command string) bool {
	text := command
	if script := scriptPath(command); script != "" {
		if body, ok := readCapped(script); ok {
			text = command + "\n" + body
		}
	}
	text = stripComments(text)
	if !agentDeckCall.MatchString(text) {
		return false
	}
	m := verbPattern.FindStringSubmatch(text)
	if m == nil {
		// agent-deck runs, but not a send/start/restart/launch: a trigger only
		// when it names itself with --source (update or notes jobs never wake
		// a session).
		if s := sourceArg.FindStringSubmatch(text); s != nil {
			f.Source = resolveWord(text, s[1])
			return f.Source != ""
		}
		return false
	}
	f.Verb = m[2]
	if f.Verb == "" {
		f.Verb = m[3]
	}
	if p := profileArg.FindStringSubmatch(m[1]); p != nil {
		f.TargetProfile = resolveWord(text, p[1])
	}
	if s := sourceArg.FindStringSubmatch(text[strings.Index(text, m[0]):]); s != nil {
		f.Source = resolveWord(text, s[1])
	}
	if f.Verb == "launch" {
		return true // launch creates a session; its argument is a path, not a target
	}
	target := resolveWord(text, m[5])
	f.Target = target
	if target != "" {
		f.TargetBy = "title"
		if sessionIDRe.MatchString(target) {
			f.TargetBy = "id"
		}
	}
	return true
}

// resolveWord unquotes a shell word and resolves a plain $VAR / ${VAR:-x}
// from an assignment in the same script. Anything still dynamic is
// unknown (""), never guessed.
func resolveWord(text, word string) string {
	w := unquote(word)
	if v := varRef.FindStringSubmatch(w); v != nil {
		w = lookupVar(text, v[1], v[2])
	}
	if strings.ContainsAny(w, "$`") {
		return ""
	}
	return w
}

// stripComments drops shell comment lines, so a script that only mentions
// agent-deck in a comment is not a trigger.
func stripComments(text string) string {
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func lookupVar(text, name, fallback string) string {
	val := ""
	for _, m := range assignment.FindAllStringSubmatch(text, -1) {
		if m[1] == name {
			val = unquote(m[2])
		}
	}
	if v := varRef.FindStringSubmatch(val); v != nil && v[1] == name {
		val = v[2] // VAR=${VAR:-default}
	}
	if val == "" {
		val = fallback
	}
	return val
}

// scriptPath returns the script a command runs: the first absolute path
// argument (after an interpreter such as /bin/bash) that is a regular file.
func scriptPath(command string) string {
	for _, tok := range strings.Fields(command) {
		tok = unquote(tok)
		if !strings.HasPrefix(tok, "/") && !strings.HasPrefix(tok, "~/") {
			continue
		}
		if strings.HasPrefix(tok, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				tok = filepath.Join(home, tok[2:])
			}
		}
		switch filepath.Base(tok) {
		case "sh", "bash", "zsh", "dash", "env", "python", "python3", "agent-deck":
			continue
		}
		if st, err := os.Stat(tok); err == nil && st.Mode().IsRegular() {
			return tok
		}
	}
	return ""
}

// --- systemd ---

func unitKeys(body string) map[string][]string {
	keys := map[string][]string{}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' || line[0] == '[' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		keys[strings.TrimSpace(k)] = append(keys[strings.TrimSpace(k)], strings.TrimSpace(v))
	}
	return keys
}

func scanSystemd(dir string, budget *int) []Found {
	if dir == "" {
		return nil
	}
	files := listDir(dir, budget, ".service", ".timer", ".path")
	present := map[string]bool{}
	for _, f := range files {
		present[filepath.Base(f)] = true
	}
	var out []Found
	for _, path := range files {
		if !strings.HasSuffix(path, ".service") {
			continue
		}
		if f, ok := scanOne(path, func() (Found, bool) { return systemdUnit(dir, path, present) }); ok {
			out = append(out, f)
		}
	}
	return out
}

// scanOne reads one unit file; a file that makes the parser panic is
// skipped, so one bad file written by another app never breaks the scan.
func scanOne(path string, read func() (Found, bool)) (f Found, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			f, ok = Found{}, false
		}
	}()
	return read()
}

func systemdUnit(dir, path string, present map[string]bool) (Found, bool) {
	body, ok := readCapped(path)
	if !ok {
		return Found{}, false
	}
	keys := unitKeys(body)
	unit := strings.TrimSuffix(filepath.Base(path), ".service")
	f := Found{Kind: KindSystemd, Unit: unit, ConfigPath: path}
	command := strings.TrimLeft(strings.Join(keys["ExecStart"], "\n"), "-@:+!")
	if home, err := os.UserHomeDir(); err == nil {
		command = strings.ReplaceAll(command, "%h", home)
	}
	if !describe(&f, command) {
		return Found{}, false
	}
	f.Schedule = systemdSchedule(dir, unit, present)
	return f, true
}

func systemdSchedule(dir, unit string, present map[string]bool) string {
	var parts []string
	if present[unit+".timer"] {
		if body, ok := readCapped(filepath.Join(dir, unit+".timer")); ok {
			k := unitKeys(body)
			for _, key := range []string{"OnCalendar", "OnUnitActiveSec", "OnUnitInactiveSec", "OnBootSec", "OnStartupSec", "OnActiveSec"} {
				for _, v := range k[key] {
					parts = append(parts, timerPhrase(key, v))
				}
			}
		}
	}
	if present[unit+".path"] {
		if body, ok := readCapped(filepath.Join(dir, unit+".path")); ok {
			k := unitKeys(body)
			for _, key := range []string{"PathChanged", "PathModified", "PathExists", "DirectoryNotEmpty"} {
				for _, v := range k[key] {
					parts = append(parts, "on change of "+filepath.Base(v))
				}
			}
		}
	}
	return strings.Join(parts, ", ")
}

func timerPhrase(key, v string) string {
	switch key {
	case "OnCalendar":
		return v
	case "OnUnitActiveSec", "OnUnitInactiveSec":
		return "every " + v
	case "OnBootSec", "OnStartupSec":
		return v + " after boot"
	}
	return v
}

// --- launchd ---

type plistValue struct {
	kind  string // string | integer | true | false | array | dict
	str   string
	array []plistValue
	dict  map[string]plistValue
}

// parsePlist reads the top-level dict of an XML property list.
func parsePlist(body []byte) (map[string]plistValue, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "dict" {
			v, err := plistDict(dec)
			return v.dict, err
		}
	}
}

func plistDict(dec *xml.Decoder) (plistValue, error) {
	v := plistValue{kind: "dict", dict: map[string]plistValue{}}
	key := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			return v, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			if t.Name.Local == "dict" {
				return v, nil
			}
		case xml.StartElement:
			if t.Name.Local == "key" {
				var s string
				if err := dec.DecodeElement(&s, &t); err != nil {
					return v, err
				}
				key = strings.TrimSpace(s)
				continue
			}
			child, err := plistElem(dec, t)
			if err != nil {
				return v, err
			}
			v.dict[key] = child
		}
	}
}

func plistElem(dec *xml.Decoder, t xml.StartElement) (plistValue, error) {
	switch t.Name.Local {
	case "dict":
		return plistDict(dec)
	case "array":
		v := plistValue{kind: "array"}
		for {
			tok, err := dec.Token()
			if err != nil {
				return v, err
			}
			if et, ok := tok.(xml.EndElement); ok && et.Name.Local == "array" {
				return v, nil
			}
			if st, ok := tok.(xml.StartElement); ok {
				child, err := plistElem(dec, st)
				if err != nil {
					return v, err
				}
				v.array = append(v.array, child)
			}
		}
	case "true", "false":
		return plistValue{kind: t.Name.Local}, dec.Skip()
	default:
		var s string
		err := dec.DecodeElement(&s, &t)
		return plistValue{kind: t.Name.Local, str: strings.TrimSpace(s)}, err
	}
}

func scanLaunchd(dir string, budget *int) []Found {
	if dir == "" {
		return nil
	}
	var out []Found
	for _, path := range listDir(dir, budget, ".plist") {
		if f, ok := scanOne(path, func() (Found, bool) { return launchdJob(path) }); ok {
			out = append(out, f)
		}
	}
	return out
}

func launchdJob(path string) (Found, bool) {
	body, ok := readCapped(path)
	if !ok {
		return Found{}, false
	}
	pl, err := parsePlist([]byte(body))
	if err != nil {
		return Found{}, false
	}
	label := pl["Label"].str
	if label == "" {
		label = strings.TrimSuffix(filepath.Base(path), ".plist")
	}
	var args []string
	for _, a := range pl["ProgramArguments"].array {
		args = append(args, a.str)
	}
	if p := pl["Program"].str; p != "" && len(args) == 0 {
		args = []string{p}
	}
	command := strings.Join(args, " ")
	f := Found{Kind: KindLaunchd, Unit: label, ConfigPath: path}
	if !describe(&f, command) {
		return Found{}, false
	}
	f.Schedule = launchdSchedule(pl)
	return f, true
}

func launchdSchedule(pl map[string]plistValue) string {
	var parts []string
	if v := pl["StartInterval"]; v.str != "" {
		if n, err := strconv.Atoi(v.str); err == nil {
			parts = append(parts, "every "+humanSeconds(n))
		}
	}
	if v, ok := pl["StartCalendarInterval"]; ok {
		cals := []plistValue{v}
		if v.kind == "array" {
			cals = v.array
		}
		for _, c := range cals {
			parts = append(parts, calendarPhrase(c.dict))
		}
	}
	for _, w := range pl["WatchPaths"].array {
		parts = append(parts, "on change of "+filepath.Base(w.str))
	}
	if pl["RunAtLoad"].kind == "true" && len(parts) == 0 {
		parts = append(parts, "at login")
	}
	if pl["KeepAlive"].kind == "true" {
		parts = append(parts, "always running")
	}
	return strings.Join(parts, ", ")
}

func humanSeconds(n int) string {
	switch {
	case n%3600 == 0:
		return fmt.Sprintf("%dh", n/3600)
	case n%60 == 0:
		return fmt.Sprintf("%dm", n/60)
	}
	return fmt.Sprintf("%ds", n)
}

func calendarPhrase(d map[string]plistValue) string {
	get := func(k string) (int, bool) {
		n, err := strconv.Atoi(d[k].str)
		return n, err == nil
	}
	h, hasH := get("Hour")
	m, hasM := get("Minute")
	wd, hasWD := get("Weekday")
	day := "daily"
	if hasWD {
		// launchd: 0 and 7 are Sunday; anything else out of range is not
		// trusted (another app's plist), so it never indexes out of range.
		day = "weekly " + []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}[((wd%7)+7)%7]
	}
	switch {
	case hasH:
		return fmt.Sprintf("%s %02d:%02d", day, h, m)
	case hasM:
		return fmt.Sprintf("hourly at :%02d", m)
	}
	return day
}
