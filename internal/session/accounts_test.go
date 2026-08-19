package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeAccountsConfig points HOME at a temp dir, writes config.toml there, and
// clears the config cache. Every test in this file uses it, so nothing here
// ever reads or writes the real ~/.agent-deck/config.toml.
func writeAccountsConfig(t *testing.T, contents string) string {
	t.Helper()
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("AGENTDECK_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	agentDeckDir := filepath.Join(tmpHome, ".agent-deck")
	if err := os.MkdirAll(agentDeckDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(agentDeckDir, "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	return tmpHome
}

// ashesh-shaped fixture: the reference multi-account setup this feature is
// designed around — several claude accounts by config_dir plus several codex
// accounts by codex_home, all under one ~/.agent-accounts tree.
const referenceAccountsConfig = `
[profiles.personal.claude]
config_dir = "~/.agent-accounts/claude/personal"

[profiles.work.claude]
config_dir = "~/.agent-accounts/claude/work"

[profiles.buddii.claude]
config_dir = "~/.agent-accounts/claude/buddii"

[profiles.seminno.claude]
config_dir = "~/.agent-accounts/claude/seminno"

[profiles.codex-gmail.codex]
codex_home = "~/.agent-accounts/codex/gmail"

[profiles.codex-seminno.codex]
codex_home = "~/.agent-accounts/codex/semanticinnovations"
`

// TestProfileCodexHomeKeyIsHonoured is the regression guard for the key that
// the reference config actually uses. Before this feature
// [profiles.<n>.codex] only accepted `config_dir`, so a config written with
// `codex_home` parsed into an EMPTY block: the key landed in the decoder's
// undecoded set, --account resolved to no home, every codex session silently
// ran against ~/.codex, and a SaveUserConfig round-trip would have dropped the
// lines entirely.
func TestProfileCodexHomeKeyIsHonoured(t *testing.T) {
	home := writeAccountsConfig(t, referenceAccountsConfig)
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	got := cfg.GetProfileCodexConfigDir("codex-seminno")
	want := filepath.Join(home, ".agent-accounts/codex/semanticinnovations")
	if got != want {
		t.Fatalf("codex_home not honoured: got %q, want %q", got, want)
	}
}

func TestProfileCodexConfigDirStillWorksAsAlias(t *testing.T) {
	home := writeAccountsConfig(t, `
[profiles.legacy.codex]
config_dir = "~/.codex-legacy"
`)
	cfg, _ := LoadUserConfig()
	want := filepath.Join(home, ".codex-legacy")
	if got := cfg.GetProfileCodexConfigDir("legacy"); got != want {
		t.Fatalf("legacy config_dir alias broke: got %q, want %q", got, want)
	}
}

func TestProfileCodexHomeBeatsConfigDirWhenBothSet(t *testing.T) {
	home := writeAccountsConfig(t, `
[profiles.both.codex]
codex_home  = "~/.codex-new"
config_dir  = "~/.codex-old"
`)
	cfg, _ := LoadUserConfig()
	want := filepath.Join(home, ".codex-new")
	if got := cfg.GetProfileCodexConfigDir("both"); got != want {
		t.Fatalf("codex_home should win over the config_dir alias: got %q, want %q", got, want)
	}
}

func TestProfileDeepSeekHomeKeyIsHonoured(t *testing.T) {
	home := writeAccountsConfig(t, `
[profiles.dsh-work.deepseek]
dsh_home = "~/.agent-accounts/dsh/work"
`)
	cfg, _ := LoadUserConfig()
	want := filepath.Join(home, ".agent-accounts/dsh/work")
	if got := cfg.GetProfileDeepSeekConfigDir("dsh-work"); got != want {
		t.Fatalf("dsh_home not honoured: got %q, want %q", got, want)
	}
}

// TestAccountsForToolIsFamilyScoped is the guard for the TUI picker's core
// promise: a codex session is never offered a claude account and vice versa.
func TestAccountsForToolIsFamilyScoped(t *testing.T) {
	writeAccountsConfig(t, referenceAccountsConfig)
	cfg, _ := LoadUserConfig()

	claude := AccountNamesForTool(cfg, "claude")
	wantClaude := "buddii,personal,seminno,work"
	if strings.Join(claude, ",") != wantClaude {
		t.Errorf("claude accounts = %v, want %s (sorted)", claude, wantClaude)
	}

	codex := AccountNamesForTool(cfg, "codex")
	wantCodex := "codex-gmail,codex-seminno"
	if strings.Join(codex, ",") != wantCodex {
		t.Errorf("codex accounts = %v, want %s (sorted)", codex, wantCodex)
	}

	// A tool with no account family gets nothing, not everything.
	if got := AccountNamesForTool(cfg, "gemini"); got != nil {
		t.Errorf("gemini has no account family; got %v, want nil", got)
	}
	if got := AccountNamesForTool(cfg, "shell"); got != nil {
		t.Errorf("shell has no account family; got %v, want nil", got)
	}
}

// TestOptInByPresence is the guard for design constraint #1: a user with no
// configured accounts must see nothing new. Every surface keys off these two
// predicates, so if they are wrong for an empty config the whole feature leaks
// into a zero-config install.
func TestOptInByPresence(t *testing.T) {
	writeAccountsConfig(t, "[claude]\ndangerous_mode = true\n")
	cfg, _ := LoadUserConfig()

	if HasAnyAccounts(cfg) {
		t.Error("HasAnyAccounts on an account-free config must be false")
	}
	for _, tool := range []string{"claude", "codex", "deepseek", "gemini", "shell"} {
		if HasAccountsForTool(cfg, tool) {
			t.Errorf("HasAccountsForTool(%q) on an account-free config must be false", tool)
		}
	}

	// And the mixed case from the REAL reference config: codex accounts
	// configured, claude accounts not. The claude side must stay invisible.
	writeAccountsConfig(t, `
[profiles.codex-gmail.codex]
codex_home = "~/.agent-accounts/codex/gmail"
`)
	ClearUserConfigCache()
	cfg2, _ := LoadUserConfig()
	if !HasAccountsForTool(cfg2, "codex") {
		t.Error("codex must have accounts here")
	}
	if HasAccountsForTool(cfg2, "claude") {
		t.Error("claude has no configured accounts here; the field must stay hidden for claude sessions")
	}
	if !HasAnyAccounts(cfg2) {
		t.Error("HasAnyAccounts must be true when any family has one")
	}
}

// TestResolveSessionAccountPrecedence walks every level of the chain.
func TestResolveSessionAccountPrecedence(t *testing.T) {
	writeAccountsConfig(t, `default_account = "personal"
`+referenceAccountsConfig+`
[groups."work"]
account = "work"

[groups."work/clientx"]
create = true

[conductors.lilu]
account = "buddii"
`)
	cfg, _ := LoadUserConfig()

	cases := []struct {
		name       string
		explicit   string
		tool       string
		group      string
		conductor  string
		wantAcct   string
		wantSource AccountSource
	}{
		{"explicit beats everything", "seminno", "claude", "work", "lilu", "seminno", AccountSourceExplicit},
		{"conductor beats group", "", "claude", "work", "lilu", "buddii", AccountSourceConductor},
		{"group beats global", "", "claude", "work", "", "work", AccountSourceGroup},
		{"group walks ancestors", "", "claude", "work/clientx", "", "work", AccountSourceGroup},
		{"global when nothing else", "", "claude", "other", "", "personal", AccountSourceGlobal},
		{"unknown conductor falls through", "", "claude", "other", "nosuch", "personal", AccountSourceGlobal},
		{"same chain for codex", "codex-seminno", "codex", "work", "lilu", "codex-seminno", AccountSourceExplicit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := ResolveSessionAccount(cfg, tc.explicit, tc.tool, tc.group, tc.conductor)
			if res.Account != tc.wantAcct {
				t.Errorf("account = %q, want %q", res.Account, tc.wantAcct)
			}
			if res.Source != tc.wantSource {
				t.Errorf("source = %q, want %q", res.Source, tc.wantSource)
			}
		})
	}
}

// TestResolveSessionAccountNoneWhenUnconfigured pins the pre-feature default:
// nothing configured anywhere means no account and no env var, so the tool
// launches against its own home exactly as before.
func TestResolveSessionAccountNoneWhenUnconfigured(t *testing.T) {
	writeAccountsConfig(t, "")
	cfg, _ := LoadUserConfig()
	res := ResolveSessionAccount(cfg, "", "claude", "any/group", "any-conductor")
	if res.Account != "" || res.Source != AccountSourceNone || res.Home != "" {
		t.Fatalf("unconfigured resolution must be inert, got %+v", res)
	}
}

// TestResolveSessionAccountCrossFamilyIsReportedNotSwallowed: handing a codex
// account to a claude session resolves (the user asked for it) but reports
// Known() == false so the caller can warn instead of silently using a
// different account.
func TestResolveSessionAccountCrossFamilyIsReportedNotSwallowed(t *testing.T) {
	writeAccountsConfig(t, referenceAccountsConfig)
	cfg, _ := LoadUserConfig()

	res := ResolveSessionAccount(cfg, "codex-seminno", "claude", "", "")
	if res.Account != "codex-seminno" {
		t.Fatalf("account = %q, want the name the user typed", res.Account)
	}
	if res.Known() {
		t.Error("a codex account on a claude session must report Known() == false")
	}
	if res.Home != "" {
		t.Errorf("home = %q, want empty for a cross-family account", res.Home)
	}

	// The matching-family case is Known().
	ok := ResolveSessionAccount(cfg, "codex-seminno", "codex", "", "")
	if !ok.Known() || ok.EnvVar != "CODEX_HOME" {
		t.Errorf("codex account on a codex session must be known with CODEX_HOME, got %+v", ok)
	}
}

// TestAccountHomeForToolMapsEachFamilyToItsEnvVar is the "tool -> env var ->
// home" contract the docs promise for adding a new tool.
func TestAccountHomeForToolMapsEachFamilyToItsEnvVar(t *testing.T) {
	home := writeAccountsConfig(t, referenceAccountsConfig+`
[profiles.dsh-work.deepseek]
dsh_home = "~/.agent-accounts/dsh/work"
`)
	cfg, _ := LoadUserConfig()

	cases := []struct {
		tool, account, wantEnv, wantHome string
	}{
		{"claude", "work", "CLAUDE_CONFIG_DIR", filepath.Join(home, ".agent-accounts/claude/work")},
		{"codex", "codex-gmail", "CODEX_HOME", filepath.Join(home, ".agent-accounts/codex/gmail")},
		{"deepseek", "dsh-work", "DSH_HOME", filepath.Join(home, ".agent-accounts/dsh/work")},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			family, ok := AccountFamilyForTool(tc.tool)
			if !ok {
				t.Fatalf("no family for %q", tc.tool)
			}
			if family.EnvVar != tc.wantEnv {
				t.Errorf("env var = %q, want %q", family.EnvVar, tc.wantEnv)
			}
			if got := AccountHomeForTool(cfg, tc.account, tc.tool); got != tc.wantHome {
				t.Errorf("home = %q, want %q", got, tc.wantHome)
			}
		})
	}
}

// TestAccountLoginStateIsCheapAndCorrect pins the on-disk probe: presence of
// the credential file, never its contents, never a network call.
func TestAccountLoginStateIsCheapAndCorrect(t *testing.T) {
	home := writeAccountsConfig(t, `
[profiles.in.codex]
codex_home = "~/acct/in"
[profiles.out.codex]
codex_home = "~/acct/out"
[profiles.missing.codex]
codex_home = "~/acct/missing"
`)
	mustMkdir(t, filepath.Join(home, "acct/in"))
	mustMkdir(t, filepath.Join(home, "acct/out"))
	if err := os.WriteFile(filepath.Join(home, "acct/in/auth.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _ := LoadUserConfig()
	byName := map[string]Account{}
	for _, a := range AccountsForTool(cfg, "codex") {
		byName[a.Name] = a
	}

	if got := byName["in"].Login; got != AccountLoginIn {
		t.Errorf("account with auth.json: login = %q, want %q", got, AccountLoginIn)
	}
	if got := byName["out"].Login; got != AccountLoginOut {
		t.Errorf("account without auth.json: login = %q, want %q", got, AccountLoginOut)
	}
	if byName["missing"].HomeExists {
		t.Error("a home that does not exist must report HomeExists false")
	}
	if got := byName["missing"].Login; got != AccountLoginUnknown {
		t.Errorf("nonexistent home: login = %q, want %q", got, AccountLoginUnknown)
	}
}

func TestClaudeLoginStateDetectsBothMarkers(t *testing.T) {
	dir := t.TempDir()
	if got := claudeLoginState(dir); got != AccountLoginOut {
		t.Errorf("empty dir: got %q, want %q", got, AccountLoginOut)
	}

	credDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(credDir, ".credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := claudeLoginState(credDir); got != AccountLoginIn {
		t.Errorf(".credentials.json: got %q, want %q", got, AccountLoginIn)
	}

	oauthDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(oauthDir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"x@y.z"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := claudeLoginState(oauthDir); got != AccountLoginIn {
		t.Errorf(".claude.json oauthAccount: got %q, want %q", got, AccountLoginIn)
	}

	nullDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(nullDir, ".claude.json"), []byte(`{"oauthAccount":null}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := claudeLoginState(nullDir); got != AccountLoginOut {
		t.Errorf("null oauthAccount: got %q, want %q", got, AccountLoginOut)
	}
}

// --- env application --------------------------------------------------------

// TestAccountAppliesCodexHomeAtLaunch is one half of the "both env
// applications" acceptance: the resolved account must reach the launched
// process as CODEX_HOME=<account home>, not just sit on the record.
func TestAccountAppliesCodexHomeAtLaunch(t *testing.T) {
	home := writeAccountsConfig(t, referenceAccountsConfig)

	inst := NewInstanceWithTool("acct-codex", t.TempDir(), "codex")
	inst.Account = "codex-seminno"
	wantHome := filepath.Join(home, ".agent-accounts/codex/semanticinnovations")

	if got := inst.accountCodexHomeDir(); got != wantHome {
		t.Fatalf("accountCodexHomeDir = %q, want %q", got, wantHome)
	}
	if got := inst.codexHomeToExport(); got != wantHome {
		t.Fatalf("codexHomeToExport = %q, want %q", got, wantHome)
	}

	cmd := inst.buildCodexCommand("codex")
	if !strings.Contains(cmd, "CODEX_HOME="+wantHome+" ") {
		t.Fatalf("launch command must export the account home.\ngot: %s\nwant substring: CODEX_HOME=%s", cmd, wantHome)
	}

	// #1929 mirror: the resume gate must resolve against the SAME home the
	// launch exports, or a restart silently opens a fresh conversation.
	if got := inst.codexHomeForCommand("codex"); got != wantHome {
		t.Fatalf("codexHomeForCommand = %q, want %q (resume gate must agree with the launch)", got, wantHome)
	}
}

// TestAccountAppliesClaudeConfigDirAtLaunch is the other half: the same
// account slot must drive CLAUDE_CONFIG_DIR for a claude session.
func TestAccountAppliesClaudeConfigDirAtLaunch(t *testing.T) {
	home := writeAccountsConfig(t, referenceAccountsConfig)

	inst := NewInstanceWithTool("acct-claude", t.TempDir(), "claude")
	inst.Account = "work"
	wantDir := filepath.Join(home, ".agent-accounts/claude/work")

	if got := GetClaudeConfigDirForInstance(inst); got != wantDir {
		t.Fatalf("GetClaudeConfigDirForInstance = %q, want %q", got, wantDir)
	}
	if _, source := GetClaudeConfigDirSourceForInstance(inst); source != "account" {
		t.Fatalf("resolution source = %q, want %q", source, "account")
	}
}

// TestAccountSelectionIsInertWithoutAnAccount pins that a session with no
// account keeps the exact pre-feature resolution.
func TestAccountSelectionIsInertWithoutAnAccount(t *testing.T) {
	writeAccountsConfig(t, referenceAccountsConfig)

	inst := NewInstanceWithTool("no-acct", t.TempDir(), "codex")
	if got := inst.accountCodexHomeDir(); got != "" {
		t.Errorf("accountCodexHomeDir with no account = %q, want empty", got)
	}
	if got := inst.codexHomeToExport(); got != "" {
		t.Errorf("codexHomeToExport with no account and no explicit CODEX_HOME = %q, want empty (codex uses its own default)", got)
	}
	if strings.Contains(inst.buildCodexCommand("codex"), "CODEX_HOME=") {
		t.Error("a session with no account must not get a CODEX_HOME= prefix")
	}
}

// --- config-first defaults --------------------------------------------------

func TestApplyAccountDefaultFillsFromGroup(t *testing.T) {
	writeAccountsConfig(t, referenceAccountsConfig+`
[groups."work"]
account = "work"
`)
	inst := NewInstanceWithGroupAndTool("s", t.TempDir(), "work/clientx", "claude")
	res := ApplyAccountDefault(inst)
	if inst.Account != "work" {
		t.Fatalf("instance account = %q, want %q (inherited from the ancestor group)", inst.Account, "work")
	}
	if res.Source != AccountSourceGroup {
		t.Fatalf("source = %q, want %q", res.Source, AccountSourceGroup)
	}
}

func TestApplyAccountDefaultLeavesExplicitAlone(t *testing.T) {
	writeAccountsConfig(t, `default_account = "personal"
`+referenceAccountsConfig+`
[groups."work"]
account = "work"
`)
	inst := NewInstanceWithGroupAndTool("s", t.TempDir(), "work", "claude")
	inst.Account = "seminno" // as if --account seminno
	res := ApplyAccountDefault(inst)
	if inst.Account != "seminno" {
		t.Fatalf("explicit account was overwritten: got %q", inst.Account)
	}
	if res.Source != AccountSourceExplicit {
		t.Fatalf("source = %q, want %q", res.Source, AccountSourceExplicit)
	}
}

func TestApplyAccountDefaultIsInertWithNoConfig(t *testing.T) {
	writeAccountsConfig(t, "")
	inst := NewInstanceWithGroupAndTool("s", t.TempDir(), "anything", "claude")
	if res := ApplyAccountDefault(inst); res.Account != "" || inst.Account != "" {
		t.Fatalf("no config must leave the account empty, got %q / %+v", inst.Account, res)
	}
}

// --- fork inheritance -------------------------------------------------------

// TestForkInheritsParentAccount covers every tool-specific fork constructor
// reachable through CreateForkedInstanceForTool. A fork resumes the PARENT's
// transcript, which lives in the parent's account home — a fork that resolved
// its own account would look in the wrong directory (#1929's failure shape).
func TestForkInheritsParentAccount(t *testing.T) {
	writeAccountsConfig(t, referenceAccountsConfig)

	t.Run("claude", func(t *testing.T) {
		parent := NewInstanceWithTool("p", t.TempDir(), "claude")
		parent.Account = "work"
		parent.ClaudeSessionID = "11111111-2222-3333-4444-555555555555"
		parent.ClaudeDetectedAt = time.Now()
		MarkClaudeSessionIDVerified(parent)
		forked, _, err := parent.CreateForkedInstanceForTool("p-fork", "", nil)
		if err != nil {
			t.Fatalf("fork: %v", err)
		}
		if forked.Account != "work" {
			t.Fatalf("claude fork account = %q, want %q", forked.Account, "work")
		}
	})
}

// TestForkOfAccountlessParentStaysAccountless: inheritance must not invent an
// account for a parent that never had one.
func TestForkOfAccountlessParentStaysAccountless(t *testing.T) {
	writeAccountsConfig(t, referenceAccountsConfig)
	parent := NewInstanceWithTool("p", t.TempDir(), "claude")
	parent.ClaudeSessionID = "11111111-2222-3333-4444-555555555555"
	parent.ClaudeDetectedAt = time.Now()
	MarkClaudeSessionIDVerified(parent)
	forked, _, err := parent.CreateForkedInstanceForTool("p-fork", "", nil)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if forked.Account != "" {
		t.Fatalf("fork of an accountless parent got account %q, want empty", forked.Account)
	}
}

// --- accounts list backing --------------------------------------------------

func TestAccountsInUseGroupsLiveSessions(t *testing.T) {
	a := NewInstanceWithTool("a", t.TempDir(), "codex")
	a.Account = "codex-gmail"
	b := NewInstanceWithTool("b", t.TempDir(), "codex")
	b.Account = "codex-gmail"
	c := NewInstanceWithTool("c", t.TempDir(), "claude")
	// no account

	inUse := AccountsInUse([]*Instance{a, b, c, nil})
	if got := len(inUse["codex-gmail"]); got != 2 {
		t.Fatalf("codex-gmail sessions = %d, want 2", got)
	}
	if _, ok := inUse[""]; ok {
		t.Fatal("accountless sessions must not be bucketed under the empty name")
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}
