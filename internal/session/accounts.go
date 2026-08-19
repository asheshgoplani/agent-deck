package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Account selection — the generalization of the per-session account slot
// (#924) from "a Claude config_dir" into "the config home of whichever tool
// family the session runs".
//
// The whole feature rests on ONE idea, deliberately kept small enough that a
// future tool joins by adding a single entry to accountFamilies below:
//
//	tool family  ->  env var  ->  per-account home directory
//
// A tool qualifies if it locates all of its own state (credentials, history,
// config) under one directory named by one environment variable. Claude does
// (CLAUDE_CONFIG_DIR), codex does (CODEX_HOME), the DeepSeek harness does
// (DSH_HOME). For such a tool, "which account am I logged in as" is entirely a
// function of which directory the process is pointed at, so selecting an
// account is selecting a directory — and agent-deck can do that generically.
//
// OPT-IN BY PRESENCE is the other load-bearing rule. Everything account-shaped
// in the UI and the CLI help is gated on HasAccountsForTool: a user with no
// [profiles.<name>.<tool>] home configured sees exactly the pre-feature
// product, with no new field in the new-session dialog and no new help text.

// AccountFamily describes how one tool family binds an account slot to a
// config home. It is the single source of truth shared by the resolver, the
// TUI picker, `agent-deck accounts list`, and the docs.
type AccountFamily struct {
	// Name is the family id. It is also the [profiles.<account>.<Name>]
	// sub-table that carries the home for an account in this family.
	Name string

	// EnvVar is the environment variable the launch exports to point the
	// tool at the account's home.
	EnvVar string

	// HomeKey is the TOML key inside [profiles.<account>.<Name>] that names
	// the home. Named after the env var wherever the tool has an established
	// spelling (codex_home, dsh_home); "config_dir" otherwise.
	HomeKey string

	// HomeKeyAliases lists additional accepted spellings of HomeKey, kept so
	// existing configs keep parsing. Documentation always shows HomeKey.
	HomeKeyAliases []string

	// Matches reports whether a session tool belongs to this family. Custom
	// [tools.*] entries with compatible_with = "claude"/"codex" match their
	// family, so a custom wrapper tool inherits account selection for free.
	Matches func(tool string) bool

	// Home returns the configured home for one account, expanded, or "" when
	// the account names no home in this family.
	Home func(cfg *UserConfig, account string) string

	// loginProbe reports the login state of a home directory using a cheap
	// stat only — never a network call and never reading a credential's
	// contents. Returns AccountLoginUnknown when the family has no reliable
	// on-disk marker.
	loginProbe func(home string) AccountLoginState
}

// AccountLoginState is the cheaply-detectable login state of an account home.
type AccountLoginState string

const (
	// AccountLoginUnknown means the family has no on-disk marker to check,
	// or the home does not exist yet.
	AccountLoginUnknown AccountLoginState = "unknown"
	// AccountLoginIn means a credential file is present in the home.
	AccountLoginIn AccountLoginState = "logged-in"
	// AccountLoginOut means the home exists but carries no credential file.
	AccountLoginOut AccountLoginState = "logged-out"
)

// accountFamilies is the registry. Adding a tool means adding one entry here
// and one ProfileXSettings block in userconfig.go — nothing else in the
// feature is tool-aware.
var accountFamilies = []AccountFamily{
	{
		Name:    "claude",
		EnvVar:  "CLAUDE_CONFIG_DIR",
		HomeKey: "config_dir",
		Matches: IsClaudeCompatible,
		Home: func(cfg *UserConfig, account string) string {
			return cfg.GetProfileClaudeConfigDir(account)
		},
		loginProbe: claudeLoginState,
	},
	{
		Name:           "codex",
		EnvVar:         "CODEX_HOME",
		HomeKey:        "codex_home",
		HomeKeyAliases: []string{"config_dir"},
		Matches:        IsCodexCompatible,
		Home: func(cfg *UserConfig, account string) string {
			return cfg.GetProfileCodexConfigDir(account)
		},
		loginProbe: markerLoginState("auth.json"),
	},
	{
		Name:           "deepseek",
		EnvVar:         "DSH_HOME",
		HomeKey:        "dsh_home",
		HomeKeyAliases: []string{"config_dir"},
		Matches:        func(tool string) bool { return tool == "deepseek" },
		Home: func(cfg *UserConfig, account string) string {
			return cfg.GetProfileDeepSeekConfigDir(account)
		},
		loginProbe: markerLoginState("auth.json"),
	},
}

// AccountFamilies returns the registered tool families, in display order.
func AccountFamilies() []AccountFamily {
	out := make([]AccountFamily, len(accountFamilies))
	copy(out, accountFamilies)
	return out
}

// AccountFamilyForTool returns the family a session tool belongs to.
//
// Order matters only for tools that could plausibly match twice; the families
// are mutually exclusive in practice, and the first match wins deterministically
// because accountFamilies is a fixed slice, not a map.
func AccountFamilyForTool(tool string) (AccountFamily, bool) {
	tool = strings.TrimSpace(tool)
	if tool == "" {
		return AccountFamily{}, false
	}
	for _, f := range accountFamilies {
		if f.Matches != nil && f.Matches(tool) {
			return f, true
		}
	}
	return AccountFamily{}, false
}

// AccountFamilyByName returns a family by its registry name.
func AccountFamilyByName(name string) (AccountFamily, bool) {
	name = strings.TrimSpace(name)
	for _, f := range accountFamilies {
		if f.Name == name {
			return f, true
		}
	}
	return AccountFamily{}, false
}

// Account is one configured account slot within one tool family.
type Account struct {
	// Name is the [profiles.<Name>] key — the value passed to --account.
	Name string `json:"name"`
	// Family is the tool family this entry configures ("claude", "codex", …).
	Family string `json:"family"`
	// EnvVar is the environment variable a session bound to this account
	// launches with.
	EnvVar string `json:"env_var"`
	// Home is the expanded home directory the env var is set to.
	Home string `json:"home"`
	// HomeExists reports whether Home is present on disk right now. A missing
	// home is not an error: the launch creates it.
	HomeExists bool `json:"home_exists"`
	// Login is the cheaply-detected login state of Home.
	Login AccountLoginState `json:"login"`
}

// AccountsForFamily returns every account configured for one family, sorted by
// name. Returns nil when the family has no accounts — which is the
// opt-in-by-presence signal every surface keys off.
func AccountsForFamily(cfg *UserConfig, family AccountFamily) []Account {
	if cfg == nil || len(cfg.Profiles) == 0 || family.Home == nil {
		return nil
	}
	names := make([]string, 0, len(cfg.Profiles))
	for name := range cfg.Profiles {
		if strings.TrimSpace(family.Home(cfg, name)) != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	out := make([]Account, 0, len(names))
	for _, name := range names {
		home := family.Home(cfg, name)
		exists := false
		if info, err := os.Stat(home); err == nil && info.IsDir() {
			exists = true
		}
		login := AccountLoginUnknown
		if exists && family.loginProbe != nil {
			login = family.loginProbe(home)
		}
		out = append(out, Account{
			Name:       name,
			Family:     family.Name,
			EnvVar:     family.EnvVar,
			Home:       home,
			HomeExists: exists,
			Login:      login,
		})
	}
	return out
}

// AccountsForTool returns the accounts compatible with a session tool: the
// claude profiles for a claude-compatible tool, the codex profiles for a
// codex-compatible one, and nil for a tool with no account family (shell,
// gemini, …) or with no configured accounts.
func AccountsForTool(cfg *UserConfig, tool string) []Account {
	family, ok := AccountFamilyForTool(tool)
	if !ok {
		return nil
	}
	return AccountsForFamily(cfg, family)
}

// AccountNamesForTool is AccountsForTool reduced to names, for help text and
// error hints.
func AccountNamesForTool(cfg *UserConfig, tool string) []string {
	accounts := AccountsForTool(cfg, tool)
	if len(accounts) == 0 {
		return nil
	}
	names := make([]string, 0, len(accounts))
	for _, a := range accounts {
		names = append(names, a.Name)
	}
	return names
}

// HasAccountsForTool is the opt-in-by-presence predicate. Every account-shaped
// surface — the TUI field, the CLI help block, the hints — is gated on it, so a
// user who has configured no accounts for the tool in hand sees no trace of the
// feature.
func HasAccountsForTool(cfg *UserConfig, tool string) bool {
	return len(AccountsForTool(cfg, tool)) > 0
}

// HasAnyAccounts reports whether any family has at least one configured
// account. Used by tool-agnostic surfaces (top-level help) that cannot key off
// a specific tool.
func HasAnyAccounts(cfg *UserConfig) bool {
	for _, f := range accountFamilies {
		if len(AccountsForFamily(cfg, f)) > 0 {
			return true
		}
	}
	return false
}

// AllAccounts returns every configured account across every family, grouped by
// family in registry order and sorted by name within each.
func AllAccounts(cfg *UserConfig) []Account {
	var out []Account
	for _, f := range accountFamilies {
		out = append(out, AccountsForFamily(cfg, f)...)
	}
	return out
}

// AccountHomeForTool returns the home an account resolves to for a given tool,
// or "" when the account names no home in that tool's family. This is the one
// function that answers "does --account X mean anything for -c Y".
func AccountHomeForTool(cfg *UserConfig, account, tool string) string {
	family, ok := AccountFamilyForTool(tool)
	if !ok || cfg == nil || strings.TrimSpace(account) == "" {
		return ""
	}
	return family.Home(cfg, account)
}

// AccountSource names the config level a resolved account came from. Reported
// by the CLI and used in tests to assert precedence rather than just the value.
type AccountSource string

const (
	// AccountSourceNone means no level named an account; the tool launches
	// against its own default home, exactly as before this feature.
	AccountSourceNone AccountSource = "none"
	// AccountSourceExplicit is --account / the TUI picker / an inherited
	// parent account.
	AccountSourceExplicit AccountSource = "explicit"
	// AccountSourceConductor is [conductors.<name>].account.
	AccountSourceConductor AccountSource = "conductor"
	// AccountSourceGroup is [groups."<path>"].account (ancestor-walking).
	AccountSourceGroup AccountSource = "group"
	// AccountSourceGlobal is the top-level default_account.
	AccountSourceGlobal AccountSource = "global"
)

// AccountResolution is the outcome of ResolveSessionAccount.
type AccountResolution struct {
	// Account is the resolved account name, or "" when nothing named one.
	Account string
	// Source names the level that supplied Account.
	Source AccountSource
	// Home is the config home Account maps to for the tool, or "" when the
	// account is unknown to the tool's family.
	Home string
	// EnvVar is the variable Home will be exported as, or "" when the tool
	// has no account family.
	EnvVar string
}

// Known reports whether the resolved account actually maps to a home for the
// tool. A false here with a non-empty Account is a typo or a cross-family
// mistake (a codex account handed to a claude session) and is worth warning
// about at the call site.
func (r AccountResolution) Known() bool {
	return r.Account != "" && r.Home != ""
}

// ResolveSessionAccount picks the account slot for a NEW session.
//
// Precedence, most-specific first:
//
//  1. explicit  — --account, the TUI picker, or a fork's inherited parent
//  2. conductor — [conductors.<name>].account
//  3. group     — [groups."<path>"].account, walking ancestors
//  4. global    — top-level default_account
//  5. none      — the tool's own default home; pre-feature behaviour
//
// Conductor-beats-group is deliberate: it is the invariant the rest of
// config.toml already holds ("conductor beats group on every key", and the
// same order in the existing config_dir chains). A conducting parent is also
// picked per-session in the new-session dialog, which makes it strictly more
// specific than the session's group.
//
// A level that names an account with no home in this tool's family is NOT
// skipped — it resolves and reports Known() == false, so the caller can say
// "you asked for codex-seminno on a claude session" instead of silently
// falling through to a different account than the user asked for.
func ResolveSessionAccount(cfg *UserConfig, explicit, tool, groupPath, conductorName string) AccountResolution {
	res := AccountResolution{Source: AccountSourceNone}
	family, hasFamily := AccountFamilyForTool(tool)
	if hasFamily {
		res.EnvVar = family.EnvVar
	}

	switch {
	case strings.TrimSpace(explicit) != "":
		res.Account, res.Source = strings.TrimSpace(explicit), AccountSourceExplicit
	case cfg == nil:
		// nothing else to consult
	default:
		if name := cfg.GetConductorAccount(conductorName); name != "" {
			res.Account, res.Source = name, AccountSourceConductor
		} else if name := cfg.GetGroupAccount(groupPath); name != "" {
			res.Account, res.Source = name, AccountSourceGroup
		} else if name := strings.TrimSpace(cfg.DefaultAccount); name != "" {
			res.Account, res.Source = name, AccountSourceGlobal
		}
	}

	if res.Account != "" && hasFamily && cfg != nil {
		res.Home = family.Home(cfg, res.Account)
	}
	return res
}

// --- login probes -----------------------------------------------------------

// markerLoginState builds a probe that reports logged-in when a named
// credential file exists directly under the home. Presence only — the file is
// never opened, so no credential material is read.
func markerLoginState(marker string) func(string) AccountLoginState {
	return func(home string) AccountLoginState {
		if home == "" {
			return AccountLoginUnknown
		}
		if _, err := os.Stat(filepath.Join(home, marker)); err == nil {
			return AccountLoginIn
		}
		return AccountLoginOut
	}
}

// claudeLoginState probes a Claude config dir. Claude Code stores an OAuth
// credential at <dir>/.credentials.json on Linux; on macOS the token lives in
// the Keychain and only the account record in <dir>/.claude.json marks the
// login, so both markers count and the account record is checked for the
// oauthAccount key rather than its value.
func claudeLoginState(home string) AccountLoginState {
	if home == "" {
		return AccountLoginUnknown
	}
	if _, err := os.Stat(filepath.Join(home, ".credentials.json")); err == nil {
		return AccountLoginIn
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		return AccountLoginOut
	}
	var probe struct {
		OAuthAccount json.RawMessage `json:"oauthAccount"`
	}
	if json.Unmarshal(raw, &probe) == nil && len(probe.OAuthAccount) > 0 && string(probe.OAuthAccount) != "null" {
		return AccountLoginIn
	}
	return AccountLoginOut
}

// --- instance wiring --------------------------------------------------------

// ApplyAccountDefault fills in a NEW instance's account slot from config when
// nothing more specific already set it, and reports the level that supplied the
// value.
//
// Resolution is done ONCE, at create time, and the answer is persisted on the
// instance. That is deliberate: the account slot records which account a
// session was BORN under, so a later edit to [groups."x"].account re-homes new
// sessions without silently moving a live conversation to different
// credentials. The config_dir/CODEX_HOME chains below the slot stay live, as
// they always were.
//
// A caller that already set inst.Account (--account, the TUI picker, a fork
// inheriting its parent) is left alone — that is the explicit level.
func ApplyAccountDefault(inst *Instance) AccountResolution {
	if inst == nil {
		return AccountResolution{Source: AccountSourceNone}
	}
	cfg, err := LoadUserConfig()
	if err != nil {
		cfg = nil
	}
	res := ResolveSessionAccount(cfg, inst.Account, inst.Tool, inst.GroupPath, conductorNameFromInstance(inst))
	if res.Account != "" {
		inst.Account = res.Account
	}
	return res
}

// AccountsInUse maps account name -> the live sessions currently bound to it.
// Sessions with no account are not counted. Used by `agent-deck accounts list`
// to answer "who is on this account right now".
func AccountsInUse(instances []*Instance) map[string][]*Instance {
	out := make(map[string][]*Instance)
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if name := strings.TrimSpace(inst.Account); name != "" {
			out[name] = append(out[name], inst)
		}
	}
	return out
}
