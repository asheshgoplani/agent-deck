// Package configdoctor compares what config.toml DECLARES against what the
// agent homes on disk actually CONTAIN, and reports the gaps.
//
// Agent Deck provisions Claude and Codex homes from declarative per-group
// loadouts, but provisioning is an attach-only floor applied at session
// create/start: removing an entry from config.toml never detaches it, and an
// entry added to config.toml does not materialize until the next session in
// that group starts. Both halves of that contract cause silent divergence —
// entries declared for one tool but never the other, plugins hand-installed
// into a home and invisible to config.toml, a Codex home that lost its notify
// hook and stopped reporting turn completion.
//
// The doctor is read-only. It never repairs; it prints what diverged and the
// command or edit that would converge it.
package configdoctor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Severity ranks a finding for both display order and the process exit code.
type Severity string

const (
	// SeverityError marks divergence that breaks a session at runtime — a
	// Codex home with no notify hook, a loadout naming a catalog key that
	// does not exist. These fail the run.
	SeverityError Severity = "error"
	// SeverityWarn marks divergence that silently changes behavior — a
	// declared entry that never materialized, an asymmetry between the two
	// tools in the same group.
	SeverityWarn Severity = "warn"
	// SeverityInfo marks state worth knowing that is often deliberate —
	// something installed in a home that config.toml does not declare.
	SeverityInfo Severity = "info"
)

var severityRank = map[Severity]int{SeverityError: 0, SeverityWarn: 1, SeverityInfo: 2}

// errNoSuchHome distinguishes "this directory is not there" from "this
// directory is there and unreadable". Only the former is tolerable, and only
// for a home Agent Deck inferred rather than one the user configured.
var errNoSuchHome = errors.New("no agent home")

// Finding is one divergence between declared and actual configuration.
type Finding struct {
	Severity Severity `json:"severity"`
	// Check is a stable kebab-case slug so findings can be filtered or
	// suppressed by category without matching on prose.
	Check string `json:"check"`
	// Scope is the group path, home directory, or catalog key the finding is
	// about — whatever the reader needs to go look at.
	Scope   string `json:"scope"`
	Summary string `json:"summary"`
	// Fix is the concrete command or config edit that converges this finding.
	// Empty when the right resolution depends on intent the doctor cannot infer.
	Fix string `json:"fix,omitempty"`
}

// Report is the full diagnosis.
type Report struct {
	Findings []Finding `json:"findings"`
	// Checked counts the homes the doctor could actually read, so an empty
	// report reads as "nothing diverged" rather than "nothing was examined".
	Checked int `json:"homes_checked"`
}

// Errors reports how many findings would fail a CI gate.
func (r Report) Errors() int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == SeverityError {
			n++
		}
	}
	return n
}

// codexHome is the parsed subset of a Codex CODEX_HOME/config.toml the doctor
// compares against. Everything else in that file is Codex's business.
type codexHome struct {
	Notify       []string                  `toml:"notify"`
	MCPServers   map[string]toml.Primitive `toml:"mcp_servers"`
	Plugins      map[string]codexPluginRow `toml:"plugins"`
	Marketplaces map[string]codexMarketRow `toml:"marketplaces"`
}

type codexPluginRow struct {
	Enabled bool `toml:"enabled"`
}

type codexMarketRow struct {
	Source string `toml:"source"`
}

// claudeHome is the parsed subset of a Claude CLAUDE_CONFIG_DIR/settings.json.
type claudeHome struct {
	EnabledPlugins map[string]bool `json:"enabledPlugins"`
}

// Diagnose runs every check against cfg and the homes it resolves to.
// homeGuard bounds filesystem access to real, absolute paths; a home that
// cannot be read is reported rather than skipped, because "I could not look"
// and "I looked and it was fine" must never print the same.
func Diagnose(cfg *session.UserConfig) Report {
	var report Report
	if cfg == nil {
		return report
	}

	groups := sortedGroupPaths(cfg)

	// Codex homes are keyed by resolved path: several groups can share one
	// home through config_dir inheritance, and a shared home must be read and
	// reported once, not once per group.
	codexHomes := map[string][]string{}
	for _, g := range groups {
		if home := cfg.GetGroupCodexConfigDir(g); home != "" {
			codexHomes[home] = append(codexHomes[home], g)
		}
	}

	report.Findings = append(report.Findings, checkToolAsymmetry(cfg, groups)...)
	report.Findings = append(report.Findings, checkCatalogRefs(cfg, groups)...)

	marketplaceSources := map[string]map[string]string{}

	for _, home := range sortedKeys(codexHomes) {
		parsed, err := readCodexHome(home)
		if err != nil {
			report.Findings = append(report.Findings, Finding{
				Severity: SeverityError,
				Check:    "home-unreadable",
				Scope:    home,
				Summary:  fmt.Sprintf("Codex home could not be read: %v", err),
			})
			continue
		}
		report.Checked++
		owners := codexHomes[home]
		report.Findings = append(report.Findings, checkCodexNotify(home, owners, parsed)...)
		report.Findings = append(report.Findings, checkCodexMaterialized(cfg, home, owners, parsed)...)
		report.Findings = append(report.Findings, checkCodexUndeclared(cfg, home, owners, parsed, groups)...)

		for name, row := range parsed.Marketplaces {
			if row.Source == "" {
				continue
			}
			if marketplaceSources[name] == nil {
				marketplaceSources[name] = map[string]string{}
			}
			marketplaceSources[name][row.Source] = home
		}
	}

	claudeHomes := map[string][]string{}
	// explicit tracks homes named in config.toml. An implicit ~/.claude that
	// does not exist means the user simply does not run Claude here, which is
	// not a finding; a configured home that does not exist always is.
	explicit := map[string]bool{}
	for _, g := range groups {
		home := cfg.GetGroupClaudeConfigDir(g)
		isExplicit := home != ""
		if home == "" {
			home = session.ExpandPath(cfg.Claude.ConfigDir)
			isExplicit = home != ""
		}
		if home == "" {
			home = defaultClaudeHome()
		}
		if home == "" {
			continue
		}
		claudeHomes[home] = append(claudeHomes[home], g)
		if isExplicit {
			explicit[home] = true
		}
	}
	for _, home := range sortedKeys(claudeHomes) {
		parsed, err := readClaudeHome(home)
		if err != nil {
			if !explicit[home] && errors.Is(err, errNoSuchHome) {
				continue
			}
			report.Findings = append(report.Findings, Finding{
				Severity: SeverityError,
				Check:    "home-unreadable",
				Scope:    home,
				Summary:  fmt.Sprintf("Claude home could not be read: %v", err),
			})
			continue
		}
		report.Checked++
		report.Findings = append(report.Findings, checkClaudeMaterialized(cfg, home, claudeHomes[home], parsed)...)
	}

	report.Findings = append(report.Findings, checkMarketplaceConflicts(marketplaceSources)...)

	sortFindings(report.Findings)
	return report
}

// checkToolAsymmetry is the headline check: an entry declared for one tool in
// a group but not the other. Not every asymmetry is a bug — plenty of plugins
// are genuinely Claude-only — so these are warnings naming both sides and
// letting the reader judge.
func checkToolAsymmetry(cfg *session.UserConfig, groups []string) []Finding {
	var out []Finding
	for _, g := range groups {
		group := cfg.Groups[g]

		// Compare only what the group itself declares. Comparing resolved
		// unions would re-report an ancestor's asymmetry on every descendant.
		claudeSkills := set(group.Claude.Skills)
		codexSkills := set(group.Codex.Skills)
		for _, s := range sortedKeys(claudeSkills) {
			if !codexSkills[s] && len(group.Codex.Skills) > 0 {
				out = append(out, Finding{
					Severity: SeverityWarn,
					Check:    "tool-asymmetry",
					Scope:    g,
					Summary:  fmt.Sprintf("skill %q is declared for Claude but not Codex", s),
					Fix:      fmt.Sprintf("add %q to [groups.%q.codex].skills, or move it to [codex].skills if it belongs everywhere", s, g),
				})
			}
		}
		for _, s := range sortedKeys(codexSkills) {
			if !claudeSkills[s] && len(group.Claude.Skills) > 0 {
				out = append(out, Finding{
					Severity: SeverityWarn,
					Check:    "tool-asymmetry",
					Scope:    g,
					Summary:  fmt.Sprintf("skill %q is declared for Codex but not Claude", s),
					Fix:      fmt.Sprintf("add %q to [groups.%q.claude].skills, or move it to [claude].skills", s, g),
				})
			}
		}

		claudeMCPs := set(group.Claude.MCPs)
		codexMCPs := set(group.Codex.MCPs)
		for _, m := range sortedKeys(claudeMCPs) {
			if !codexMCPs[m] && len(group.Codex.MCPs) > 0 {
				out = append(out, Finding{
					Severity: SeverityWarn,
					Check:    "tool-asymmetry",
					Scope:    g,
					Summary:  fmt.Sprintf("MCP %q is declared for Claude but not Codex", m),
					Fix:      fmt.Sprintf("add %q to [groups.%q.codex].mcps", m, g),
				})
			}
		}
		for _, m := range sortedKeys(codexMCPs) {
			if !claudeMCPs[m] && len(group.Claude.MCPs) > 0 {
				out = append(out, Finding{
					Severity: SeverityWarn,
					Check:    "tool-asymmetry",
					Scope:    g,
					Summary:  fmt.Sprintf("MCP %q is declared for Codex but not Claude", m),
					Fix:      fmt.Sprintf("add %q to [groups.%q.claude].mcps", m, g),
				})
			}
		}
	}
	return out
}

// checkCatalogRefs catches loadout entries naming a [mcps.X] or [plugins.X]
// key that does not exist. ApplyConfiguredLoadout only warns into the debug
// log for these, so they are otherwise invisible.
func checkCatalogRefs(cfg *session.UserConfig, groups []string) []Finding {
	var out []Finding
	available := session.GetAvailableMCPs()

	seen := map[string]bool{}
	report := func(scope, kind, name, where string) {
		key := scope + "|" + kind + "|" + name
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Finding{
			Severity: SeverityError,
			Check:    "unknown-catalog-ref",
			Scope:    scope,
			Summary:  fmt.Sprintf("%s references %s %q, which has no [%s.%s] definition", where, kind, name, kind, name),
			Fix:      fmt.Sprintf("define [%s.%s] in config.toml or remove the reference", kind, name),
		})
	}

	for _, name := range cfg.Claude.MCPs {
		if _, ok := available[name]; !ok {
			report("[claude]", "mcps", name, "the global Claude loadout")
		}
	}
	for _, name := range cfg.Codex.MCPs {
		if _, ok := available[name]; !ok {
			report("[codex]", "mcps", name, "the global Codex loadout")
		}
	}
	for _, name := range cfg.Claude.Plugins {
		if session.GetPluginDef(name) == nil {
			report("[claude]", "plugins", name, "the global Claude loadout")
		}
	}

	for _, g := range groups {
		group := cfg.Groups[g]
		for _, name := range group.Claude.MCPs {
			if _, ok := available[name]; !ok {
				report(g, "mcps", name, fmt.Sprintf("group %q (claude)", g))
			}
		}
		for _, name := range group.Codex.MCPs {
			if _, ok := available[name]; !ok {
				report(g, "mcps", name, fmt.Sprintf("group %q (codex)", g))
			}
		}
		for _, name := range group.Claude.Plugins {
			if session.GetPluginDef(name) == nil {
				report(g, "plugins", name, fmt.Sprintf("group %q (claude)", g))
			}
		}
	}
	return out
}

// checkCodexNotify catches the failure that looks like a hung session: a Codex
// home with no notify program never tells Agent Deck a turn finished, so the
// session sits in the wrong state until someone opens the pane.
func checkCodexNotify(home string, owners []string, parsed codexHome) []Finding {
	if len(parsed.Notify) > 0 {
		return nil
	}
	return []Finding{{
		Severity: SeverityError,
		Check:    "codex-notify-missing",
		Scope:    home,
		Summary: fmt.Sprintf("Codex home has no notify program — sessions in %s will not report turn completion to Agent Deck",
			strings.Join(owners, ", ")),
		Fix: fmt.Sprintf("CODEX_HOME=%s agent-deck codex-hooks install (or copy the notify line from a healthy sibling home)", home),
	}}
}

// checkCodexMaterialized reports declared entries that never landed in the
// home. Expected right after a config edit — the loadout materializes on the
// next session start — so the fix says so instead of implying breakage.
func checkCodexMaterialized(cfg *session.UserConfig, home string, owners []string, parsed codexHome) []Finding {
	var out []Finding
	root := owner(owners)

	for _, name := range sortedKeys(declaredAcross(owners, cfg.GetGroupCodexMCPs)) {
		if _, ok := parsed.MCPServers[name]; !ok {
			out = append(out, Finding{
				Severity: SeverityWarn,
				Check:    "declared-not-materialized",
				Scope:    home,
				Summary:  fmt.Sprintf("MCP %q is declared for %s but absent from the home's [mcp_servers]", name, root),
				Fix:      "start a session in that group to materialize it, or check the loadout warnings in the debug log",
			})
		}
	}
	for _, sel := range sortedKeys(declaredAcross(owners, cfg.GetGroupCodexPlugins)) {
		if _, ok := parsed.Plugins[sel]; !ok {
			out = append(out, Finding{
				Severity: SeverityWarn,
				Check:    "declared-not-materialized",
				Scope:    home,
				Summary:  fmt.Sprintf("plugin %q is declared for %s but absent from the home's [plugins]", sel, root),
				Fix:      fmt.Sprintf("agent-deck group codex sync %s", root),
			})
		}
	}
	for _, skill := range sortedKeys(declaredAcross(owners, cfg.GetGroupCodexSkills)) {
		if !skillMaterialized(home, skill) {
			out = append(out, Finding{
				Severity: SeverityWarn,
				Check:    "declared-not-materialized",
				Scope:    home,
				Summary:  fmt.Sprintf("skill %q is declared for %s but absent from %s/skills", skill, root, home),
				Fix:      "start a session in that group to attach it, or run `agent-deck skill source add` if the source is unregistered",
			})
		}
	}
	return out
}

// checkCodexUndeclared reports what a home carries that config.toml does not
// know about. Informational by design: hand-installing into a home is a valid
// thing to do, and the doctor's job is to make it visible, not to forbid it.
func checkCodexUndeclared(cfg *session.UserConfig, home string, owners []string, parsed codexHome, groups []string) []Finding {
	var out []Finding
	root := owner(owners)

	declaredMCPs := declaredAcross(owners, cfg.GetGroupCodexMCPs)
	for _, name := range sortedKeys(parsed.MCPServers) {
		if !declaredMCPs[name] {
			out = append(out, Finding{
				Severity: SeverityInfo,
				Check:    "undeclared-in-home",
				Scope:    home,
				Summary:  fmt.Sprintf("MCP %q is present in the home but declared nowhere in config.toml", name),
				Fix:      fmt.Sprintf("add %q to [groups.%q.codex].mcps (or [codex].mcps) to make it reproducible, or remove it from the home", name, root),
			})
		}
	}

	declaredPlugins := declaredAcross(owners, cfg.GetGroupCodexPlugins)
	for _, sel := range sortedKeys(parsed.Plugins) {
		if !parsed.Plugins[sel].Enabled || declaredPlugins[sel] {
			continue
		}
		out = append(out, Finding{
			Severity: SeverityInfo,
			Check:    "undeclared-in-home",
			Scope:    home,
			Summary:  fmt.Sprintf("plugin %q is enabled in the home but declared nowhere in config.toml", sel),
			Fix:      fmt.Sprintf("add %q to [codex].plugins if it belongs everywhere, or to [groups.%q.codex].plugins", sel, root),
		})
	}
	return out
}

// checkClaudeMaterialized compares declared Claude plugins against the home's
// settings.json. Claude MCPs are deliberately not checked here: they are
// written per-project into .mcp.json at session start, so a home-level absence
// proves nothing about whether the loadout is working.
func checkClaudeMaterialized(cfg *session.UserConfig, home string, owners []string, parsed claudeHome) []Finding {
	var out []Finding
	root := owner(owners)

	// Map every declared catalog key to the selector Claude actually writes
	// into settings.json, so the reverse check below can tell "undeclared" from
	// "declared under a different catalog key".
	declaredSelectors := map[string]string{}
	for _, key := range sortedKeys(declaredAcross(owners, cfg.GetGroupClaudePlugins)) {
		def := session.GetPluginDef(key)
		if def == nil {
			continue // already reported by checkCatalogRefs
		}
		selector := def.Name + "@" + def.Source
		declaredSelectors[selector] = key
		if !parsed.EnabledPlugins[selector] {
			out = append(out, Finding{
				Severity: SeverityWarn,
				Check:    "declared-not-materialized",
				Scope:    home,
				Summary:  fmt.Sprintf("plugin %q (%s) is declared for %s but not enabled in this Claude home", key, selector, root),
				Fix:      fmt.Sprintf("enable it with `claude plugin install %s`, or drop it from the group loadout", selector),
			})
		}
	}

	for _, selector := range sortedKeys(parsed.EnabledPlugins) {
		if !parsed.EnabledPlugins[selector] || declaredSelectors[selector] != "" {
			continue
		}
		out = append(out, Finding{
			Severity: SeverityInfo,
			Check:    "undeclared-in-home",
			Scope:    home,
			Summary:  fmt.Sprintf("plugin %q is enabled in this Claude home but declared nowhere in config.toml", selector),
			Fix:      fmt.Sprintf("add a [plugins.X] catalog entry and list it under [claude].plugins or [groups.%q.claude].plugins, or disable it", root),
		})
	}
	return out
}

// checkMarketplaceConflicts catches one marketplace name resolving to
// different sources in different homes — the same plugin selector then means
// different code depending on which home a session lands in.
func checkMarketplaceConflicts(sources map[string]map[string]string) []Finding {
	var out []Finding
	for _, name := range sortedKeys(sources) {
		bySource := sources[name]
		if len(bySource) < 2 {
			continue
		}
		var parts []string
		for _, src := range sortedKeys(bySource) {
			parts = append(parts, fmt.Sprintf("%s (in %s)", src, bySource[src]))
		}
		out = append(out, Finding{
			Severity: SeverityWarn,
			Check:    "marketplace-source-conflict",
			Scope:    name,
			Summary:  fmt.Sprintf("marketplace %q resolves to %d different sources: %s", name, len(bySource), strings.Join(parts, "; ")),
			Fix:      "point every home at one source, or rename one marketplace so the selectors stay distinguishable",
		})
	}
	return out
}

func readCodexHome(home string) (codexHome, error) {
	var parsed codexHome
	path, err := safeJoin(home, "config.toml")
	if err != nil {
		return parsed, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return parsed, fmt.Errorf("no config.toml at %s", path)
	}
	if _, err := toml.DecodeFile(path, &parsed); err != nil {
		return parsed, fmt.Errorf("parse %s: %w", path, err)
	}
	return parsed, nil
}

func readClaudeHome(home string) (claudeHome, error) {
	var parsed claudeHome
	path, err := safeJoin(home, "settings.json")
	if err != nil {
		return parsed, err
	}
	// A home directory that does not exist was never examined, and must not be
	// counted as a clean check — "I could not look" and "I looked and it was
	// fine" have to read differently.
	if info, statErr := os.Stat(home); statErr != nil || !info.IsDir() {
		return parsed, fmt.Errorf("%w at %s", errNoSuchHome, home)
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// The home exists but has no settings.json yet: a legitimate fresh
		// state with no enabled plugins, not an unreadable home.
		return parsed, nil
	}
	if err != nil {
		return parsed, err
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return parsed, fmt.Errorf("parse %s: %w", path, err)
	}
	return parsed, nil
}

func skillMaterialized(home, skill string) bool {
	// Loadout entries are "<source>/<name>"; only the trailing name becomes
	// the directory under the home's skills/.
	name := skill
	if idx := strings.LastIndex(skill, "/"); idx >= 0 {
		name = skill[idx+1:]
	}
	path, err := safeJoin(home, filepath.Join("skills", name))
	if err != nil {
		return false
	}
	_, statErr := os.Lstat(path)
	return statErr == nil
}

// safeJoin refuses relative or traversing home paths before any filesystem
// access. Home paths reach here from config.toml, which is user-owned but not
// necessarily user-reviewed after a sync.
func safeJoin(home, rel string) (string, error) {
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("home path %q is not absolute", home)
	}
	if strings.Contains(home, "..") {
		return "", fmt.Errorf("home path %q contains parent traversal", home)
	}
	return filepath.Join(home, rel), nil
}

func defaultClaudeHome() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

// owner returns the shallowest group sharing a home — the one that declares
// config_dir and from which every other owner inherits it. Using the shallowest
// (not the longest) path matters for the fix text: telling someone to edit
// [groups."uniqcast/highlight-event-manager".codex] when the home belongs to
// "uniqcast" sends them to the wrong stanza.
func owner(groups []string) string {
	best := ""
	for _, g := range groups {
		if best == "" || len(g) < len(best) || (len(g) == len(best) && g < best) {
			best = g
		}
	}
	return best
}

// declaredAcross unions a resolver over every group sharing a home. A home is
// shared through config_dir inheritance, so its contents legitimately serve all
// of them; checking against only one owner would report a sibling's entry as
// undeclared.
func declaredAcross(groups []string, resolve func(string) []string) map[string]bool {
	out := map[string]bool{}
	for _, g := range groups {
		for _, entry := range resolve(g) {
			out[entry] = true
		}
	}
	return out
}

func sortedGroupPaths(cfg *session.UserConfig) []string {
	out := make([]string, 0, len(cfg.Groups))
	for g := range cfg.Groups {
		out = append(out, g)
	}
	sort.Strings(out)
	return out
}

func set(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		if v != "" {
			out[v] = true
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		if severityRank[findings[i].Severity] != severityRank[findings[j].Severity] {
			return severityRank[findings[i].Severity] < severityRank[findings[j].Severity]
		}
		if findings[i].Check != findings[j].Check {
			return findings[i].Check < findings[j].Check
		}
		if findings[i].Scope != findings[j].Scope {
			return findings[i].Scope < findings[j].Scope
		}
		return findings[i].Summary < findings[j].Summary
	})
}
