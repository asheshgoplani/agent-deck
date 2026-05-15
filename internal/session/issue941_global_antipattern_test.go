package session

// Issue #941 regression — telegram GLOBAL_ANTIPATTERN duplicate-poller spawn.
//
// When a conductor session is launched with `--channels plugin:telegram@...`
// AND the ambient profile's settings.json has
// `enabledPlugins["telegram@claude-plugins-official"] = true`, the claude
// process loads the plugin TWICE (once from the global flag, once from
// --channels). Each load spawns its own `bun telegram start` poller on the
// conductor's TELEGRAM_STATE_DIR. The two pollers race for the same bot
// token and Telegram Bot API returns 409 Conflict.
//
// The TelegramValidator already EMITS a GLOBAL_ANTIPATTERN+DOUBLE_LOAD pair
// for this topology — but warnings don't prevent the spawn. v3 topology
// (memory: telegram_channel_conductor_only.md) requires:
//
//	conductor uses --channels explicitly; enabledPlugins must be false globally
//
// ...but that contract is enforced only by docs. This test, plus the
// fix it drives, lifts it into the code path.
//
// Strategy: spawn a fake `claude` binary that mimics the plugin activation
// arithmetic — one bun poller per activation source (global flag in
// settings.json + --channels arg). Use real OS process detection (pgrep on
// a unique tag carried via TELEGRAM_STATE_DIR) to count. With the fix in
// place, `prepareWorkerScratchConfigDirForSpawn` rewrites the conductor's
// CLAUDE_CONFIG_DIR to a scratch profile that pins enabledPlugins.telegram
// off → --channels becomes the only activation → 1 poller.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTelegram_GlobalScope_OneBunPollerOnly_RegressionFor941(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("issue #941 reproduces on linux conductor hosts; pgrep/proc-environ semantics not portable")
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not available — required for real-process detection")
	}

	// Hermetic HOME so userconfig + workerscratch land in tempdirs.
	home := withTempHome(t)
	withTelegramConductorPresent(t) // makes hostHasTelegramConductor() == true

	// Ambient profile (the conductor's CLAUDE_CONFIG_DIR) — the
	// GLOBAL_ANTIPATTERN: enabledPlugins.telegram=true.
	sourceProfile := filepath.Join(home, ".claude")
	if err := os.MkdirAll(sourceProfile, 0o755); err != nil {
		t.Fatalf("mkdir source profile: %v", err)
	}
	srcSettings := `{"enabledPlugins":{"telegram@claude-plugins-official":true}}`
	if err := os.WriteFile(filepath.Join(sourceProfile, "settings.json"), []byte(srcSettings), 0o644); err != nil {
		t.Fatalf("write source settings: %v", err)
	}
	// Pin source resolution via env so GetClaudeConfigDirForInstance →
	// this dir regardless of host config.
	t.Setenv("CLAUDE_CONFIG_DIR", sourceProfile)

	// Conductor session: channel-owner with --channels telegram@...
	inst := &Instance{
		ID:          "11111111-1111-1111-1111-111111111111",
		Tool:        "claude",
		Title:       "conductor-941",
		ProjectPath: home,
		Channels:    []string{"plugin:telegram@claude-plugins-official"},
	}

	// Run the real spawn-prep path. With the fix this creates a scratch
	// CLAUDE_CONFIG_DIR pinning telegram OFF; without the fix it no-ops
	// (channel-owners are excluded from worker-scratch today).
	inst.prepareWorkerScratchConfigDirForSpawn()
	configDir := inst.WorkerScratchConfigDir
	if configDir == "" {
		configDir = sourceProfile
	}

	// Build fake binaries to simulate the plugin's spawn arithmetic.
	// We do NOT run real claude/bun — the assertion is that the spawn
	// path agent-deck prepares results in ONE poller, not two.
	binDir := t.TempDir()

	// Unique tag carried in TELEGRAM_STATE_DIR — used as the pgrep filter
	// so we count only this test's pollers, not anything else on the host.
	tag := fmt.Sprintf("tdd941-%d", time.Now().UnixNano())
	tsd := filepath.Join(t.TempDir(), tag)
	if err := os.MkdirAll(tsd, 0o755); err != nil {
		t.Fatalf("mkdir tsd: %v", err)
	}

	// Poller script — filename embeds the tag and the literal "bun-telegram"
	// marker so /proc/<pid>/cmdline carries both substrings. pgrep -af on
	// the tag will match this script's argv[0] reliably without relying on
	// non-portable exec -a tricks.
	pollerScript := filepath.Join(binDir, "bun-telegram-poller-"+tag+".sh")
	// Do NOT exec sleep — we want the shell process to stay alive with
	// the script path in /proc/<pid>/cmdline (which is what pgrep -af
	// reads). exec sleep would replace argv and lose the tag.
	if err := os.WriteFile(pollerScript, []byte(
		"#!/bin/sh\nsleep 30\n",
	), 0o755); err != nil {
		t.Fatalf("write poller: %v", err)
	}

	// fake-claude.sh — counts activation sources, then forks one poller
	// per source. Sources:
	//   (a) settings.json has `"telegram@claude-plugins-official": true`
	//   (b) --channels argv contains "plugin:telegram@"
	fakeClaude := filepath.Join(binDir, "fake-claude.sh")
	fakeClaudeBody := `#!/bin/bash
set -u
COUNT=0
if [ -n "${CLAUDE_CONFIG_DIR:-}" ] && [ -f "$CLAUDE_CONFIG_DIR/settings.json" ]; then
  if grep -Eq '"telegram@claude-plugins-official"[[:space:]]*:[[:space:]]*true' "$CLAUDE_CONFIG_DIR/settings.json"; then
    COUNT=$((COUNT+1))
  fi
fi
for a in "$@"; do
  case "$a" in
    *plugin:telegram@*) COUNT=$((COUNT+1));;
  esac
done
i=0
while [ "$i" -lt "$COUNT" ]; do
  "` + pollerScript + `" &
  i=$((i+1))
done
sleep 20
`
	if err := os.WriteFile(fakeClaude, []byte(fakeClaudeBody), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	// Spawn fake claude with the SAME env agent-deck would set:
	//   - CLAUDE_CONFIG_DIR = scratch (post-fix) or source (pre-fix)
	//   - TELEGRAM_STATE_DIR carrying our test tag
	//   - --channels echoed onto argv (matches Instance.spawnFlags())
	cmd := exec.Command(fakeClaude, "--channels", "plugin:telegram@claude-plugins-official")
	cmd.Env = append(os.Environ(),
		"CLAUDE_CONFIG_DIR="+configDir,
		"TELEGRAM_STATE_DIR="+tsd,
		"TAG="+tag,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake claude: %v", err)
	}

	// Cleanup: kill the fake claude tree and any stray pollers carrying
	// our tag. Best-effort — leaks here would only affect this test's
	// pgrep filter, not other tests.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = exec.Command("pkill", "-f", tag).Run()
	})

	// Settle. The fake forks pollers immediately, but give the kernel a
	// beat to populate /proc.
	time.Sleep(400 * time.Millisecond)

	// Real process detection: pgrep -af on the unique tag.
	out, _ := exec.Command("pgrep", "-af", tag).CombinedOutput()
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	pollerCount := 0
	for _, line := range lines {
		if !strings.Contains(line, tag) {
			continue
		}
		if !strings.Contains(line, "bun") || !strings.Contains(line, "telegram") {
			continue
		}
		pollerCount++
	}

	if pollerCount != 1 {
		t.Fatalf("issue #941 regression: expected exactly 1 bun telegram poller "+
			"(channel-owning conductor must not duplicate when global enabledPlugins.telegram=true), "+
			"got %d.\npgrep output:\n%s\nscratch=%q source=%q",
			pollerCount, string(out), inst.WorkerScratchConfigDir, sourceProfile)
	}
}
