package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// channelConfig is the input format from the bash issue-watcher's channels.json.
type channelConfig struct {
	Name        string `json:"name"`
	ProjectPath string `json:"project_path"`
	Group       string `json:"group"`
	Prefix      string `json:"prefix"`
}

// watcherListEntry is the JSON schema for `agent-deck watcher list --json`.
// Phase 21 (REQ-WF-6) adds last_event_ts, error_count, health_status.
type watcherListEntry struct {
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Status        string  `json:"status"`
	EventsPerHour float64 `json:"events_per_hour"`
	Health        string  `json:"health"`
	// Phase 21 (REQ-WF-6) additions:
	LastEventTS  *time.Time `json:"last_event_ts"`
	ErrorCount   int        `json:"error_count"`
	HealthStatus string     `json:"health_status"` // "healthy"|"warning"|"error"|"unknown"
}

// populateStateFields reads <name>/state.json and fills Phase 21 fields on e.
// Mirrors internal/watcher/health.go HealthTracker.Check() thresholds. Keep in lockstep.
func populateStateFields(e *watcherListEntry) {
	state, serr := watcher.LoadState(e.Name)
	if serr != nil || state == nil {
		e.LastEventTS = nil
		e.ErrorCount = 0
		e.HealthStatus = "unknown"
		return
	}
	ts := state.LastEventTS
	e.LastEventTS = &ts
	e.ErrorCount = state.ErrorCount
	e.HealthStatus = classifyFromState(state)
}

// classifyFromState mirrors HealthTracker.Check() thresholds at internal/watcher/health.go:163-178.
// Keep in lockstep: if health.go thresholds change, update here too.
func classifyFromState(s *watcher.WatcherState) string {
	if !s.AdapterHealthy || s.ErrorCount >= 10 {
		return "error"
	}
	if s.ErrorCount >= 3 {
		return "warning"
	}
	return "healthy"
}

// handleWatcher dispatches watcher subcommands.
func handleWatcher(profile string, args []string) {
	if len(args) == 0 {
		printWatcherHelp()
		return
	}

	switch args[0] {
	case "import":
		handleWatcherImport(profile, args[1:])
	case "create":
		handleWatcherCreate(profile, args[1:])
	case "start":
		handleWatcherStart(profile, args[1:])
	case "stop":
		handleWatcherStop(profile, args[1:])
	case "list":
		handleWatcherList(profile, args[1:])
	case "status":
		handleWatcherStatus(profile, args[1:])
	case "test":
		handleWatcherTest(profile, args[1:])
	case "routes":
		handleWatcherRoutes(profile, args[1:])
	case "install-skill":
		if err := handleWatcherInstallSkill(profile, args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "help", "--help", "-h":
		printWatcherHelp()
	default:
		fmt.Fprintf(os.Stderr, "Unknown watcher command: %s\n", args[0])
		fmt.Fprintln(os.Stderr)
		printWatcherHelp()
		os.Exit(1)
	}
}

// openWatcherDB opens the statedb for the given profile.
func openWatcherDB(profile string) (*statedb.StateDB, error) {
	dbPath, err := session.GetDBPathForProfile(profile)
	if err != nil {
		return nil, fmt.Errorf("resolve db path: %w", err)
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open statedb: %w", err)
	}
	if err := db.Migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate statedb: %w", err)
	}
	return db, nil
}

// validWatcherTypes lists all supported adapter types.
var validWatcherTypes = []string{"webhook", "ntfy", "github", "slack"}

// isValidWatcherType reports whether t is a known adapter type.
func isValidWatcherType(t string) bool {
	for _, v := range validWatcherTypes {
		if v == t {
			return true
		}
	}
	return false
}

// handleWatcherCreate creates a new watcher entry in statedb and writes meta.json.
func handleWatcherCreate(profile string, args []string) {
	fs := flag.NewFlagSet("watcher create", flag.ExitOnError)
	name := fs.String("name", "", "Watcher name (required)")
	port := fs.Int("port", 0, "Port for webhook adapter")
	topic := fs.String("topic", "", "Topic for ntfy or slack adapter")
	secret := fs.String("secret", "", "HMAC secret for github adapter")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck watcher create <type> --name <name> [options]")
		fmt.Println()
		fmt.Println("Types: webhook, ntfy, github, slack")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Fprintln(os.Stderr, "Error: adapter type is required")
		fmt.Fprintln(os.Stderr)
		fs.Usage()
		os.Exit(1)
	}
	adapterType := remaining[0]

	// Validate type (T-16-01)
	if !isValidWatcherType(adapterType) {
		fmt.Fprintf(os.Stderr, "Error: unknown adapter type %q. Valid types: %v\n", adapterType, validWatcherTypes)
		os.Exit(1)
	}

	if *name == "" {
		fmt.Fprintln(os.Stderr, "Error: --name is required")
		os.Exit(1)
	}

	// Validate type-specific required flags
	switch adapterType {
	case "webhook":
		if *port == 0 {
			fmt.Fprintln(os.Stderr, "Error: --port is required for webhook adapter")
			os.Exit(1)
		}
	case "ntfy":
		if *topic == "" {
			fmt.Fprintln(os.Stderr, "Error: --topic is required for ntfy adapter")
			os.Exit(1)
		}
	case "github":
		if *secret == "" {
			fmt.Fprintln(os.Stderr, "Error: --secret is required for github adapter")
			os.Exit(1)
		}
	case "slack":
		if *topic == "" {
			fmt.Fprintln(os.Stderr, "Error: --topic is required for slack adapter")
			os.Exit(1)
		}
	}

	db, err := openWatcherDB(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	configPath, err := session.WatcherNameDir(*name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving watcher dir: %v\n", err)
		os.Exit(1)
	}

	now := time.Now()
	row := statedb.WatcherRow{
		ID:         uuid.New().String(),
		Name:       *name,
		Type:       adapterType,
		ConfigPath: configPath,
		Status:     "stopped",
		Conductor:  "",
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if err := db.SaveWatcher(&row); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving watcher: %v\n", err)
		os.Exit(1)
	}

	if err := session.SaveWatcherMeta(&session.WatcherMeta{
		Name:      *name,
		Type:      adapterType,
		CreatedAt: now.Format(time.RFC3339),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving watcher meta: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created watcher: %s (type: %s)\n", *name, adapterType)
}

// handleWatcherStart marks a watcher as running in statedb.
func handleWatcherStart(profile string, args []string) {
	fs := flag.NewFlagSet("watcher start", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck watcher start <name>")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: watcher name is required")
		fs.Usage()
		os.Exit(1)
	}
	name := fs.Arg(0)

	db, err := openWatcherDB(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	w, err := db.LoadWatcherByName(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if w == nil {
		fmt.Fprintf(os.Stderr, "Error: watcher %q not found\n", name)
		os.Exit(1)
	}

	if err := db.UpdateWatcherStatus(w.ID, "running"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Started watcher: %s (status will be picked up by TUI engine)\n", name)
}

// handleWatcherStop marks a watcher as stopped in statedb.
func handleWatcherStop(profile string, args []string) {
	fs := flag.NewFlagSet("watcher stop", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck watcher stop <name>")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: watcher name is required")
		fs.Usage()
		os.Exit(1)
	}
	name := fs.Arg(0)

	db, err := openWatcherDB(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	w, err := db.LoadWatcherByName(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if w == nil {
		fmt.Fprintf(os.Stderr, "Error: watcher %q not found\n", name)
		os.Exit(1)
	}

	if err := db.UpdateWatcherStatus(w.ID, "stopped"); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Stopped watcher: %s\n", name)
}

// handleWatcherList lists all watchers with name, type, status, event rate, and health.
func handleWatcherList(profile string, args []string) {
	fs := flag.NewFlagSet("watcher list", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck watcher list [--json]")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	db, err := openWatcherDB(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	watchers, err := db.LoadWatchers()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading watchers: %v\n", err)
		os.Exit(1)
	}

	cutoff := time.Now().Add(-time.Hour)
	entries := make([]watcherListEntry, 0, len(watchers))
	for _, w := range watchers {
		events, _ := db.LoadWatcherEvents(w.ID, 100)
		var eventsLastHour int
		for _, e := range events {
			if e.CreatedAt.After(cutoff) {
				eventsLastHour++
			}
		}

		var health string
		switch {
		case w.Status == "stopped":
			health = "stopped"
		case eventsLastHour > 0:
			health = "healthy"
		default:
			health = "unknown"
		}

		entry := watcherListEntry{
			Name:          w.Name,
			Type:          w.Type,
			Status:        w.Status,
			EventsPerHour: float64(eventsLastHour),
			Health:        health,
		}
		populateStateFields(&entry)
		entries = append(entries, entry)
	}

	if *jsonOutput {
		out, _ := json.MarshalIndent(entries, "", "  ")
		fmt.Println(string(out))
		return
	}

	if len(entries) == 0 {
		fmt.Println("No watchers configured.")
		fmt.Println("Run 'agent-deck watcher create <type> --name <name>' to create one.")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tSTATUS\tEVENTS/HR\tHEALTH")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.0f\t%s\n", e.Name, e.Type, e.Status, e.EventsPerHour, e.Health)
	}
	tw.Flush()
}

// handleWatcherStatus shows detailed info for a named watcher including recent events.
func handleWatcherStatus(profile string, args []string) {
	fs := flag.NewFlagSet("watcher status", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck watcher status <name> [--json]")
	}

	// Extract positional arg before flags
	var name string
	var flagArgs []string
	for _, arg := range args {
		if len(arg) > 0 && arg[0] == '-' {
			flagArgs = append(flagArgs, arg)
		} else if name == "" {
			name = arg
		} else {
			flagArgs = append(flagArgs, arg)
		}
	}

	if err := fs.Parse(normalizeArgs(fs, flagArgs)); err != nil {
		os.Exit(1)
	}

	if name == "" {
		fmt.Fprintln(os.Stderr, "Error: watcher name is required")
		fs.Usage()
		os.Exit(1)
	}

	db, err := openWatcherDB(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	w, err := db.LoadWatcherByName(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if w == nil {
		fmt.Fprintf(os.Stderr, "Error: watcher %q not found\n", name)
		os.Exit(1)
	}

	meta, _ := session.LoadWatcherMeta(name)
	events, _ := db.LoadWatcherEvents(w.ID, 10)

	if *jsonOutput {
		type eventOut struct {
			Timestamp string `json:"timestamp"`
			Sender    string `json:"sender"`
			Subject   string `json:"subject"`
			RoutedTo  string `json:"routed_to"`
		}
		type output struct {
			ID         string     `json:"id"`
			Name       string     `json:"name"`
			Type       string     `json:"type"`
			Status     string     `json:"status"`
			ConfigPath string     `json:"config_path"`
			Conductor  string     `json:"conductor"`
			CreatedAt  string     `json:"created_at"`
			UpdatedAt  string     `json:"updated_at"`
			Events     []eventOut `json:"recent_events"`
		}
		out := output{
			ID:         w.ID,
			Name:       w.Name,
			Type:       w.Type,
			Status:     w.Status,
			ConfigPath: w.ConfigPath,
			Conductor:  w.Conductor,
			CreatedAt:  w.CreatedAt.Format(time.RFC3339),
			UpdatedAt:  w.UpdatedAt.Format(time.RFC3339),
		}
		for _, e := range events {
			out.Events = append(out.Events, eventOut{
				Timestamp: e.CreatedAt.Format(time.RFC3339),
				Sender:    e.Sender,
				Subject:   e.Subject,
				RoutedTo:  e.RoutedTo,
			})
		}
		data, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(data))
		return
	}

	fmt.Printf("Watcher: %s\n", w.Name)
	fmt.Printf("  Type:    %s\n", w.Type)
	fmt.Printf("  Status:  %s\n", w.Status)
	fmt.Printf("  ID:      %s\n", w.ID)
	if meta != nil {
		fmt.Printf("  Created: %s\n", meta.CreatedAt)
	}
	if w.Conductor != "" {
		fmt.Printf("  Conductor: %s\n", w.Conductor)
	}
	fmt.Println()

	if len(events) == 0 {
		fmt.Println("Recent Events: (none)")
	} else {
		fmt.Println("Recent Events:")
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "TIMESTAMP\tSENDER\tSUBJECT\tROUTED_TO")
		for _, e := range events {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
				e.CreatedAt.Format("2006-01-02 15:04:05"),
				e.Sender, e.Subject, e.RoutedTo)
		}
		tw.Flush()
	}
}

// handleWatcherTest runs a synthetic event through the router for the named watcher.
func handleWatcherTest(profile string, args []string) {
	fs := flag.NewFlagSet("watcher test", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck watcher test <name>")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "Error: watcher name is required")
		fs.Usage()
		os.Exit(1)
	}
	name := fs.Arg(0)

	db, err := openWatcherDB(profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	w, err := db.LoadWatcherByName(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if w == nil {
		fmt.Fprintf(os.Stderr, "Error: watcher %q not found\n", name)
		os.Exit(1)
	}

	// Load clients.json and build router
	router, err := watcher.LoadFromWatcherDir()
	if err != nil {
		fmt.Printf("Routing config: not available (%v)\n", err)
		fmt.Println("  Create ~/.agent-deck/watcher/clients.json to enable routing.")
		return
	}

	// Synthetic event
	testSender := "test@synthetic.local"
	result := router.Match(testSender)

	fmt.Printf("Watcher: %s (type: %s)\n", w.Name, w.Type)
	fmt.Printf("Sender:  %s\n", testSender)
	fmt.Printf("Subject: Test event from watcher test command\n")
	fmt.Printf("Time:    %s\n", time.Now().Format(time.RFC3339))
	fmt.Println()

	if result == nil {
		fmt.Println("Match:   none")
		fmt.Println("Routes to: (no matching route — would go to triage)")
	} else {
		fmt.Printf("Match:   %s\n", result.MatchType)
		fmt.Printf("Routes to conductor: %s\n", result.Conductor)
		if result.Group != "" {
			fmt.Printf("Group:   %s\n", result.Group)
		}
	}
}

// handleWatcherRoutes lists all routing rules from clients.json.
func handleWatcherRoutes(profile string, args []string) {
	fs := flag.NewFlagSet("watcher routes", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck watcher routes [--json]")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	_ = profile

	watcherDir, err := session.WatcherDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving watcher directory: %v\n", err)
		os.Exit(1)
	}
	clientsPath := filepath.Join(watcherDir, "clients.json")

	clients, err := watcher.LoadClientsJSON(clientsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading clients.json: %v\n", err)
		fmt.Fprintln(os.Stderr, "Run 'agent-deck watcher import <channels.json>' to generate client routing rules.")
		os.Exit(1)
	}

	if *jsonOutput {
		out, _ := json.MarshalIndent(clients, "", "  ")
		fmt.Println(string(out))
		return
	}

	if len(clients) == 0 {
		fmt.Println("No routing rules configured.")
		return
	}

	// Sort for stable output
	keys := make([]string, 0, len(clients))
	for k := range clients {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SENDER\tCONDUCTOR\tGROUP\tNAME")
	for _, k := range keys {
		e := clients[k]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", k, e.Conductor, e.Group, e.Name)
	}
	tw.Flush()
}

// handleWatcherImport reads a bash issue-watcher channels.json and generates
// Go watcher configuration files (watcher.toml per channel + clients.json entries).
func handleWatcherImport(profile string, args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: agent-deck watcher import <path-to-channels.json>")
		os.Exit(1)
	}

	inputPath := filepath.Clean(args[0])

	// Resolve output directory via session.WatcherDir()
	outputDir, err := session.WatcherDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving watcher directory: %v\n", err)
		os.Exit(1)
	}

	if err := importChannels(inputPath, outputDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// parseChannelsJSON reads and parses a channels.json file.
// Returns the parsed channel map or an error if the file cannot be read or parsed.
func parseChannelsJSON(path string) (map[string]channelConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read channels.json: %w", err)
	}
	var channels map[string]channelConfig
	if err := json.Unmarshal(data, &channels); err != nil {
		return nil, fmt.Errorf("parse channels.json: %w", err)
	}
	if channels == nil {
		channels = make(map[string]channelConfig)
	}
	return channels, nil
}

// generateWatcherToml creates the watcher.toml content for a channel.
func generateWatcherToml(channelID string, cfg channelConfig) string {
	return fmt.Sprintf(`# Auto-generated by: agent-deck watcher import
# Source channel: %s (%s)

[watcher]
name = %q
type = "slack"

[source]
# TODO: set your ntfy topic (all Slack channels share one ntfy topic from the Cloudflare Worker)
topic = ""
server = "https://ntfy.sh"

[routing]
conductor = %q
group = %q
`, channelID, cfg.Name, cfg.Prefix, cfg.Prefix, cfg.Group)
}

// mergeClientsJSON loads existing clients.json (if any), merges new entries, and writes back.
// Existing entries with the same key are overwritten by new entries (idempotent).
// Uses atomic write (temp file + rename) per threat model T-15-09.
func mergeClientsJSON(clientsPath string, newEntries map[string]watcher.ClientEntry) error {
	merged := make(map[string]watcher.ClientEntry)

	// Load existing if present
	if data, err := os.ReadFile(clientsPath); err == nil {
		if err := json.Unmarshal(data, &merged); err != nil {
			return fmt.Errorf("parse existing clients.json: %w", err)
		}
	}

	// Merge new entries (overwrite on key collision)
	for k, v := range newEntries {
		merged[k] = v
	}

	// Marshal with indentation
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal clients.json: %w", err)
	}

	// Atomic write: write to temp file in same directory, then rename
	dir := filepath.Dir(clientsPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create clients.json directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".clients-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for clients.json: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp clients.json: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp clients.json: %w", err)
	}
	if err := os.Rename(tmpName, clientsPath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename temp clients.json: %w", err)
	}

	return nil
}

// importChannels is the core logic for the import command.
// It reads a channels.json file, generates watcher.toml per channel,
// and creates/merges clients.json entries with slack:{CHANNEL_ID} keys.
func importChannels(inputPath, outputDir string) error {
	// Security: validate input path (T-15-07)
	cleanPath := filepath.Clean(inputPath)

	info, err := os.Lstat(cleanPath)
	if err != nil {
		return fmt.Errorf("cannot access %q: %w", cleanPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("symlink not allowed: %q", cleanPath)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %q", cleanPath)
	}

	// Parse channels
	channels, err := parseChannelsJSON(cleanPath)
	if err != nil {
		return err
	}

	// Ensure output directory exists
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// Sort channel IDs for deterministic output
	ids := make([]string, 0, len(channels))
	for id := range channels {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// Build client entries and generate watcher.toml files
	clientEntries := make(map[string]watcher.ClientEntry, len(channels))

	for _, channelID := range ids {
		cfg := channels[channelID]

		// Create watcher directory
		watcherDir := filepath.Join(outputDir, cfg.Prefix)
		if err := os.MkdirAll(watcherDir, 0o755); err != nil {
			return fmt.Errorf("create watcher dir for %q: %w", cfg.Prefix, err)
		}

		// Write watcher.toml
		tomlContent := generateWatcherToml(channelID, cfg)
		tomlPath := filepath.Join(watcherDir, "watcher.toml")
		if err := os.WriteFile(tomlPath, []byte(tomlContent), 0o644); err != nil {
			return fmt.Errorf("write watcher.toml for %q: %w", cfg.Prefix, err)
		}

		// Build client entry with slack:{CHANNEL_ID} key (D-11, D-12)
		clientKey := fmt.Sprintf("slack:%s", channelID)
		clientEntries[clientKey] = watcher.ClientEntry{
			Conductor: cfg.Prefix,
			Group:     cfg.Group,
			Name:      cfg.Name,
		}
	}

	// Merge clients.json
	clientsPath := filepath.Join(outputDir, "clients.json")
	if err := mergeClientsJSON(clientsPath, clientEntries); err != nil {
		return err
	}

	// Print summary
	fmt.Printf("Imported %d channel(s)\n", len(channels))
	for _, id := range ids {
		cfg := channels[id]
		fmt.Printf("  %s -> %s/watcher.toml\n", id, cfg.Prefix)
	}
	if len(channels) > 0 {
		fmt.Println()
		fmt.Println("Note: Set the ntfy topic in each watcher.toml (all Slack channels")
		fmt.Println("share one ntfy topic from the Cloudflare Worker).")
	}
	fmt.Printf("Clients written to: %s\n", clientsPath)

	return nil
}

// printWatcherHelp prints usage for the watcher subcommand.
func printWatcherHelp() {
	fmt.Println("Usage: agent-deck watcher <command> [args]")
	fmt.Println()
	fmt.Println("Watchers listen for inbound events (webhooks, push notifications,")
	fmt.Println("GitHub events, Slack messages) and route them to conductor sessions")
	fmt.Println("so your running agents can act on them automatically.")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  create <type> --name <name>   Create a new watcher (types: webhook, ntfy, github, slack)")
	fmt.Println("  start <name>                  Mark a watcher as running (picked up by TUI engine)")
	fmt.Println("  stop <name>                   Mark a watcher as stopped")
	fmt.Println("  list [--json]                 List all watchers with status and event rate")
	fmt.Println("  status <name> [--json]        Show detailed watcher info including recent events")
	fmt.Println("  test <name>                   Route a synthetic event to verify routing config")
	fmt.Println("  routes [--json]               Show all routing rules from clients.json")
	fmt.Println("  import <path>                 Migrate bash issue-watcher channels.json to Go watcher config")
	fmt.Println("  install-skill <skill-name>    Install a skill to ~/.agent-deck/skills/pool/ (e.g. watcher-creator)")
	fmt.Println()
	fmt.Println("Examples (one per adapter type):")
	fmt.Println()
	fmt.Println("  # Generic HTTP webhook listening on port 9000")
	fmt.Println("  agent-deck watcher create webhook --name my-webhook --port 9000")
	fmt.Println()
	fmt.Println("  # GitHub repository webhook with HMAC-SHA256 verification")
	fmt.Println("  agent-deck watcher create github --name gh-alerts --secret $GITHUB_WEBHOOK_SECRET")
	fmt.Println()
	fmt.Println("  # ntfy.sh push-notification topic")
	fmt.Println("  agent-deck watcher create ntfy --name phone-alerts --topic my-private-topic")
	fmt.Println()
	fmt.Println("  # Slack-bridged via ntfy topic (see skill for Cloudflare Worker setup)")
	fmt.Println("  agent-deck watcher create slack --name team-slack --topic my-slack-topic")
	fmt.Println()
	fmt.Println("After creating, activate and inspect:")
	fmt.Println("  agent-deck watcher start <name>")
	fmt.Println("  agent-deck watcher list              # see health + events/hour")
	fmt.Println("  agent-deck watcher test <name>       # fire a synthetic event")
	fmt.Println()
	fmt.Println("Routing: edit ~/.agent-deck/watcher/<name>/clients.json to pick which")
	fmt.Println("conductor / group receives events. See `agent-deck watcher routes` for")
	fmt.Println("the currently-loaded rules across all watchers.")
	fmt.Println()
	fmt.Println("Conversational setup (recommended for first-time use):")
	fmt.Println("  agent-deck watcher install-skill watcher-creator")
	fmt.Println("  # Then inside Claude Code in an agent-deck session, ask:")
	fmt.Println("  # \"Use the watcher-creator skill to set up a GitHub watcher\"")
	fmt.Println()
	fmt.Println("Full reference: https://github.com/asheshgoplani/agent-deck#watchers")
}
