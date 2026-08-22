package agents

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// LaunchSource is the normalized reading of a launchd plist or a systemd
// unit/timer pair.
//
// Introspection is textual. Nothing here executes, sources, or expands a
// command, and environment VALUES are never read into this struct — only the
// key names, so a report can say "this unit depends on GMAIL_APP_PASSWORD"
// without the value ever entering a definition, a log, or an export.
type LaunchSource struct {
	Kind             string   `json:"kind"` // "launchd" | "systemd"
	Path             string   `json:"path"`
	Label            string   `json:"label"`
	Program          string   `json:"program"`
	Arguments        []string `json:"arguments,omitempty"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
	// EnvKeys are names only. Values are deliberately not captured.
	EnvKeys []string `json:"env_keys,omitempty"`
	// EnvFiles are referenced env files, recorded as paths so a human can
	// audit them. They are not opened.
	EnvFiles        []string `json:"env_files,omitempty"`
	IntervalSeconds int      `json:"interval_seconds,omitempty"`
	// CalendarSpec is the source's own schedule spelling (a launchd
	// StartCalendarInterval rendered to cron, or a systemd OnCalendar).
	CalendarSpec string `json:"calendar_spec,omitempty"`
	// CalendarSpecs preserves every repeated OnCalendar directive. systemd
	// treats them as alternatives; collapsing to the last one lies about when
	// the timer fires.
	CalendarSpecs []string `json:"calendar_specs,omitempty"`
	// SourcePaths lists every unit read to produce this observation.
	SourcePaths []string `json:"source_paths,omitempty"`
	// ScheduleKey names which systemd key produced IntervalSeconds, so a
	// report can say where the cadence came from.
	ScheduleKey string `json:"schedule_key,omitempty"`
	// ScheduleSource is the timer/plist file that owns the schedule. It may
	// differ from Path when a .service target loads its sibling .timer.
	ScheduleSource string `json:"schedule_source,omitempty"`
	// HasUnrepresentableSchedule marks a source that demonstrably fires on a
	// schedule which has no exact cron equivalent. The trigger is still
	// recorded — as opaque — so the fleet view never shows an agent with
	// nothing firing it when something plainly does.
	HasUnrepresentableSchedule bool `json:"has_unrepresentable_schedule,omitempty"`
	// RawScheduleText is the source's own spelling of that schedule.
	RawScheduleText string `json:"raw_schedule_text,omitempty"`
	KeepAlive       bool   `json:"keep_alive,omitempty"`
	RunAtLoad       bool   `json:"run_at_load,omitempty"`
	RestartMode     string `json:"restart_mode,omitempty"`
	// Warnings record what could be seen but not understood.
	Warnings []string `json:"warnings,omitempty"`
}

// ProgramStatus is what we can prove about a unit's program path.
type ProgramStatus string

const (
	// ProgramPresent means the path resolves to something on this machine.
	ProgramPresent ProgramStatus = "present"
	// ProgramMissing means the path is fully resolved and is not there.
	ProgramMissing ProgramStatus = "missing"
	// ProgramUnknown means we cannot resolve the path well enough to ask.
	ProgramUnknown ProgramStatus = "unknown"
)

// unexpandedSpecifier matches a systemd specifier this reader did not resolve.
var unexpandedSpecifier = regexp.MustCompile(`%[a-zA-Z]`)

// ProgramStatus reports what can be proven about the unit's program.
//
// The distinction matters more than it looks. "Missing" is the only verdict
// that labels a unit as debris, which is the one label that invites a human to
// delete something. A path we merely could not resolve — because it still
// carries a systemd specifier this reader does not expand, such as %i or %t —
// is UNKNOWN. Reporting an unresolved token as a missing file told the user
// that running services were leftovers.
func (s *LaunchSource) ProgramStatus() ProgramStatus {
	program := strings.TrimSpace(s.Program)
	if program == "" {
		return ProgramUnknown
	}
	if unexpandedSpecifier.MatchString(program) {
		return ProgramUnknown
	}
	if !strings.ContainsAny(program, "/\\") {
		// A bare command name resolved from PATH at launch time. We cannot
		// prove absence, so we do not claim it.
		return ProgramUnknown
	}
	if _, err := os.Stat(program); err == nil {
		return ProgramPresent
	}
	return ProgramMissing
}

// expandSystemdSpecifiers resolves the specifiers whose values this process can
// know for certain, and leaves every other one in place so ProgramStatus can
// see that the path is unresolved.
//
// %h, %u, %U and %H are properties of the user and machine the unit would run
// as, which for a user unit read on its own machine is this process. %% is
// literal. Everything else — %i, %n, %t, %S, %E, %C, %v, %m, %b — depends on
// instance name or runtime context that is not knowable from the file, and
// guessing at them is how a running service gets called debris.
// isSystemScopeUnit reports whether a unit path is a system unit rather than
// one of this user's.
func isSystemScopeUnit(path string) bool {
	cleaned := filepath.ToSlash(filepath.Clean(path))
	for _, prefix := range []string{"/etc/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system", "/run/systemd/system"} {
		if strings.HasPrefix(cleaned, prefix) {
			return true
		}
	}
	return false
}

// expandSystemdSpecifiersIf expands only when the adopting process's identity
// is the identity the unit would run as.
func expandSystemdSpecifiersIf(ourIdentity bool, value string) string {
	if !ourIdentity {
		return value
	}
	return expandSystemdSpecifiers(value)
}

func expandSystemdSpecifiers(value string) string {
	if value == "" || !strings.Contains(value, "%") {
		return value
	}
	replacements := []string{"%%", "\x00PERCENT\x00"}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		replacements = append(replacements, "%h", home)
	}
	if user := os.Getenv("USER"); user != "" {
		replacements = append(replacements, "%u", user)
	}
	replacements = append(replacements, "%U", strconv.Itoa(os.Getuid()))
	if host, err := os.Hostname(); err == nil && host != "" {
		replacements = append(replacements, "%H", host)
	}
	expanded := strings.NewReplacer(replacements...).Replace(value)
	return strings.ReplaceAll(expanded, "\x00PERCENT\x00", "%")
}

// --- launchd -----------------------------------------------------------

// plistValue is a decoded plist node.
type plistValue struct {
	kind  string // "string" | "integer" | "bool" | "array" | "dict" | "other"
	str   string
	num   int
	flag  bool
	array []plistValue
	dict  map[string]plistValue
	// order preserves dict key order for stable rendering.
	order []string
}

// ParseLaunchdPlist reads an XML launchd plist.
func ParseLaunchdPlist(path string) (*LaunchSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plist: %w", err)
	}
	root, err := decodePlist(data)
	if err != nil {
		return nil, fmt.Errorf("parse plist %q: %w", path, err)
	}
	if root.kind != "dict" {
		return nil, fmt.Errorf("parse plist %q: top level is %s, want dict", path, root.kind)
	}

	src := &LaunchSource{Kind: "launchd", Path: path}
	src.Label = root.dict["Label"].str
	if src.Label == "" {
		src.Label = strings.TrimSuffix(filepath.Base(path), ".plist")
	}

	if program, ok := root.dict["Program"]; ok && program.kind == "string" {
		src.Program = program.str
	}
	if args, ok := root.dict["ProgramArguments"]; ok && args.kind == "array" {
		for _, item := range args.array {
			src.Arguments = append(src.Arguments, item.str)
		}
		if src.Program == "" && len(src.Arguments) > 0 {
			src.Program = src.Arguments[0]
		}
	}
	if wd, ok := root.dict["WorkingDirectory"]; ok {
		src.WorkingDirectory = wd.str
	}
	if env, ok := root.dict["EnvironmentVariables"]; ok && env.kind == "dict" {
		src.EnvKeys = append(src.EnvKeys, env.order...)
		sort.Strings(src.EnvKeys)
	}
	if interval, ok := root.dict["StartInterval"]; ok && interval.kind == "integer" {
		src.IntervalSeconds = interval.num
	}
	if cal, ok := root.dict["StartCalendarInterval"]; ok {
		src.CalendarSpec = renderCalendarInterval(cal)
		if src.CalendarSpec == "" {
			src.HasUnrepresentableSchedule = true
			src.RawScheduleText = describeCalendarInterval(cal)
			src.Warnings = append(src.Warnings,
				"StartCalendarInterval is present but has no exact cron equivalent "+
					"(an array of intervals, or both Day and Weekday, which launchd ANDs and cron ORs); "+
					"no next-due time is computed")
		}
	}
	if ka, ok := root.dict["KeepAlive"]; ok {
		// KeepAlive may be a bool or a dict of conditions. A dict means
		// "restart under conditions we are not modelling in phase 1".
		switch ka.kind {
		case "bool":
			src.KeepAlive = ka.flag
		case "dict":
			src.KeepAlive = true
			src.Warnings = append(src.Warnings, "KeepAlive is conditional; its conditions are not interpreted")
		}
	}
	if ral, ok := root.dict["RunAtLoad"]; ok && ral.kind == "bool" {
		src.RunAtLoad = ral.flag
	}
	if src.KeepAlive {
		src.RestartMode = "always"
	}
	return src, nil
}

// renderCalendarInterval renders a launchd StartCalendarInterval as a
// five-field cron expression, or "" when it cannot be expressed as one.
//
// Two cases return "": an ARRAY of intervals, which one cron string cannot
// express, and a dict that sets BOTH Day and Weekday. launchd ANDs those two
// (the 1st, but only when it is a Monday); cron ORs them (every 1st AND every
// Monday). Emitting the cron spelling would render a next-due time that is
// wrong for most months, at high confidence — worse than rendering none.
func renderCalendarInterval(v plistValue) string {
	if v.kind != "dict" {
		return ""
	}
	_, hasDay := v.dict["Day"]
	_, hasWeekday := v.dict["Weekday"]
	if hasDay && hasWeekday {
		return ""
	}
	field := func(key string) string {
		if item, ok := v.dict[key]; ok && item.kind == "integer" {
			return strconv.Itoa(item.num)
		}
		return "*"
	}
	return strings.Join([]string{
		field("Minute"), field("Hour"), field("Day"), field("Month"), field("Weekday"),
	}, " ")
}

// decodePlist walks the XML token stream. It intentionally supports only the
// node kinds launchd actually uses.
func decodePlist(data []byte) (plistValue, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return plistValue{}, errors.New("no <plist> element")
			}
			return plistValue{}, err
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local == "plist" {
			return decodePlistChild(dec)
		}
	}
}

// decodePlistChild reads the single value inside <plist>.
func decodePlistChild(dec *xml.Decoder) (plistValue, error) {
	for {
		tok, err := dec.Token()
		if err != nil {
			return plistValue{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return decodePlistValue(dec, t)
		case xml.EndElement:
			if t.Name.Local == "plist" {
				return plistValue{}, errors.New("empty <plist>")
			}
		}
	}
}

func decodePlistValue(dec *xml.Decoder, start xml.StartElement) (plistValue, error) {
	switch start.Name.Local {
	case "dict":
		return decodePlistDict(dec)
	case "array":
		return decodePlistArray(dec)
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return plistValue{}, err
		}
		return plistValue{kind: "bool", flag: start.Name.Local == "true"}, nil
	case "integer":
		text, err := decodeText(dec, start)
		if err != nil {
			return plistValue{}, err
		}
		num, convErr := strconv.Atoi(strings.TrimSpace(text))
		if convErr != nil {
			return plistValue{kind: "other", str: text}, nil
		}
		return plistValue{kind: "integer", num: num}, nil
	case "string", "real", "date":
		text, err := decodeText(dec, start)
		if err != nil {
			return plistValue{}, err
		}
		return plistValue{kind: "string", str: text}, nil
	default:
		// <data> and anything else: record that it existed, never its bytes.
		if err := dec.Skip(); err != nil {
			return plistValue{}, err
		}
		return plistValue{kind: "other"}, nil
	}
}

func decodeText(dec *xml.Decoder, start xml.StartElement) (string, error) {
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				return sb.String(), nil
			}
		}
	}
}

func decodePlistDict(dec *xml.Decoder) (plistValue, error) {
	result := plistValue{kind: "dict", dict: map[string]plistValue{}}
	var pendingKey string
	haveKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return plistValue{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "key" {
				text, textErr := decodeText(dec, t)
				if textErr != nil {
					return plistValue{}, textErr
				}
				pendingKey = strings.TrimSpace(text)
				haveKey = true
				continue
			}
			value, valueErr := decodePlistValue(dec, t)
			if valueErr != nil {
				return plistValue{}, valueErr
			}
			if haveKey {
				if _, exists := result.dict[pendingKey]; !exists {
					result.order = append(result.order, pendingKey)
				}
				result.dict[pendingKey] = value
				haveKey = false
			}
		case xml.EndElement:
			if t.Name.Local == "dict" {
				return result, nil
			}
		}
	}
}

func decodePlistArray(dec *xml.Decoder) (plistValue, error) {
	result := plistValue{kind: "array"}
	for {
		tok, err := dec.Token()
		if err != nil {
			return plistValue{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			value, valueErr := decodePlistValue(dec, t)
			if valueErr != nil {
				return plistValue{}, valueErr
			}
			result.array = append(result.array, value)
		case xml.EndElement:
			if t.Name.Local == "array" {
				return result, nil
			}
		}
	}
}

// --- systemd -----------------------------------------------------------

// ParseSystemdUnit reads a .service or .timer file. When given a .service that
// has a sibling .timer, the timer's schedule is folded in, because the pair is
// what actually describes the firing.
func ParseSystemdUnit(path string) (*LaunchSource, error) {
	sections, err := parseINI(path)
	if err != nil {
		return nil, err
	}

	src := &LaunchSource{
		Kind:        "systemd",
		Path:        path,
		Label:       strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		SourcePaths: []string{path},
	}

	service := sections["Service"]
	if len(service) == 0 && strings.HasSuffix(path, ".timer") {
		servicePath := strings.TrimSuffix(path, ".timer") + ".service"
		if serviceSections, serviceErr := parseINI(servicePath); serviceErr == nil {
			service = serviceSections["Service"]
			src.SourcePaths = append(src.SourcePaths, servicePath)
			src.Warnings = append(src.Warnings, "runtime read from paired service "+filepath.Base(servicePath))
		}
	}
	// %h, %u and %U belong to the identity the unit RUNS AS. That is this
	// process only for a user unit with no User= override. For a system unit,
	// or one that sets User=, expanding with the adopting process's identity
	// would resolve to the wrong path — and a wrong path resolves to
	// "missing", which is how a running service got called debris in the
	// first place. Leave them unexpanded so the answer stays UNKNOWN.
	identityIsOurs := !isSystemScopeUnit(path) && firstValue(service, "User") == ""
	if execStart := firstValue(service, "ExecStart"); execStart != "" {
		argv := splitArgv(execStart)
		if len(argv) > 0 {
			// systemd allows a leading "-", "@", "+", "!" on ExecStart.
			raw := strings.TrimLeft(argv[0], "-@+!:")
			src.Program = expandSystemdSpecifiersIf(identityIsOurs, raw)
			src.Arguments = argv
			if src.Program != raw {
				src.Warnings = append(src.Warnings,
					"ExecStart specifier expanded: "+raw+" -> "+src.Program)
			}
			if unexpandedSpecifier.MatchString(src.Program) {
				src.Warnings = append(src.Warnings,
					"ExecStart contains a systemd specifier this reader does not expand; "+
						"whether its program exists is unknown")
			}
		}
	}
	src.WorkingDirectory = expandSystemdSpecifiersIf(identityIsOurs, firstValue(service, "WorkingDirectory"))
	src.RestartMode = firstValue(service, "Restart")
	for _, env := range service["Environment"] {
		// Split only far enough to learn the key. The value is discarded
		// here and never stored.
		if key, _, found := strings.Cut(env, "="); found {
			src.EnvKeys = append(src.EnvKeys, strings.TrimSpace(strings.Trim(key, `"`)))
		}
	}
	sort.Strings(src.EnvKeys)
	src.EnvFiles = append(src.EnvFiles, service["EnvironmentFile"]...)

	timerSection := sections["Timer"]
	if len(timerSection) > 0 {
		src.ScheduleSource = path
	}
	if len(timerSection) == 0 && strings.HasSuffix(path, ".service") {
		timerPath := strings.TrimSuffix(path, ".service") + ".timer"
		if timerSections, timerErr := parseINI(timerPath); timerErr == nil {
			timerSection = timerSections["Timer"]
			src.ScheduleSource = timerPath
			src.SourcePaths = append(src.SourcePaths, timerPath)
			src.Warnings = append(src.Warnings, "schedule read from paired timer "+filepath.Base(timerPath))
		}
	}
	for _, onCalendar := range timerSection["OnCalendar"] {
		if onCalendar = strings.TrimSpace(onCalendar); onCalendar != "" {
			src.CalendarSpecs = append(src.CalendarSpecs, onCalendar)
		}
	}
	if len(src.CalendarSpecs) > 0 {
		src.CalendarSpec = src.CalendarSpecs[0]
	}
	// systemd has several monotonic cadence keys and a timer may set more
	// than one. They are read in the order that best describes the repeating
	// cadence: the gap after the last run (Active/Inactive) beats the one-off
	// delays measured from boot or startup, which only say when the FIRST run
	// happens. Reading only OnUnitActiveSec silently produced no cadence at
	// all for a timer that used OnUnitInactiveSec.
	for _, key := range []string{"OnUnitActiveSec", "OnUnitInactiveSec", "OnActiveSec", "OnBootSec", "OnStartupSec"} {
		value := firstValue(timerSection, key)
		if value == "" {
			continue
		}
		seconds, ok := parseSystemdDuration(value)
		if !ok {
			src.Warnings = append(src.Warnings, key+"="+value+" is not a duration this reader understands")
			continue
		}
		src.IntervalSeconds = seconds
		src.ScheduleKey = key
		if key == "OnBootSec" || key == "OnStartupSec" {
			src.Warnings = append(src.Warnings,
				key+" only sets when the first run happens; it is not a repeating cadence")
		}
		break
	}
	return src, nil
}

func firstValue(section map[string][]string, key string) string {
	if values := section[key]; len(values) > 0 {
		return values[len(values)-1]
	}
	return ""
}

// parseINI reads a systemd unit into section -> key -> values. Keys may repeat,
// so values accumulate.
func parseINI(path string) (map[string]map[string][]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read unit: %w", err)
	}
	sections := map[string]map[string][]string{}
	current := ""
	var continued strings.Builder

	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			current = strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			if _, ok := sections[current]; !ok {
				sections[current] = map[string][]string{}
			}
			continue
		}
		// systemd line continuations end with a backslash.
		if strings.HasSuffix(trimmed, `\`) {
			continued.WriteString(strings.TrimSuffix(trimmed, `\`))
			continued.WriteString(" ")
			continue
		}
		if continued.Len() > 0 {
			trimmed = continued.String() + trimmed
			continued.Reset()
		}
		key, value, found := strings.Cut(trimmed, "=")
		if !found || current == "" {
			continue
		}
		key = strings.TrimSpace(key)
		sections[current][key] = append(sections[current][key], strings.TrimSpace(value))
	}
	return sections, nil
}

// splitArgv splits a command line on whitespace, honoring quotes. It does not
// expand variables, globs, or command substitution — introspection must never
// evaluate shell.
// describeCalendarInterval renders a StartCalendarInterval as readable text
// for a trigger whose schedule has no cron equivalent, so the row shows the
// real terms rather than nothing.
func describeCalendarInterval(v plistValue) string {
	if v.kind == "array" {
		return fmt.Sprintf("launchd StartCalendarInterval (%d intervals)", len(v.array))
	}
	if v.kind != "dict" {
		return "launchd StartCalendarInterval"
	}
	parts := make([]string, 0, len(v.order))
	for _, key := range v.order {
		if item, ok := v.dict[key]; ok && item.kind == "integer" {
			parts = append(parts, fmt.Sprintf("%s=%d", key, item.num))
		}
	}
	if len(parts) == 0 {
		return "launchd StartCalendarInterval"
	}
	return "launchd " + strings.Join(parts, " ")
}

func splitArgv(line string) []string {
	var args []string
	var current strings.Builder
	quote := rune(0)
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t':
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

// parseSystemdDuration understands the common suffixed forms.
func parseSystemdDuration(value string) (int, bool) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, false
	}
	multipliers := []struct {
		suffix string
		factor int
	}{
		{"us", 0}, {"ms", 0},
		{"seconds", 1}, {"second", 1}, {"sec", 1}, {"s", 1},
		{"minutes", 60}, {"minute", 60}, {"min", 60}, {"m", 60},
		{"hours", 3600}, {"hour", 3600}, {"hr", 3600}, {"h", 3600},
		{"days", 86400}, {"day", 86400}, {"d", 86400},
	}
	for _, m := range multipliers {
		if !strings.HasSuffix(trimmed, m.suffix) {
			continue
		}
		if m.factor == 0 {
			return 0, false
		}
		numPart := strings.TrimSpace(strings.TrimSuffix(trimmed, m.suffix))
		num, err := strconv.Atoi(numPart)
		if err != nil {
			return 0, false
		}
		return num * m.factor, true
	}
	if num, err := strconv.Atoi(trimmed); err == nil {
		return num, true
	}
	return 0, false
}
