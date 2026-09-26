package telemetry

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func TestSpoolSkipsTornAndForgedLines(t *testing.T) {
	c := env(t)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	p, _ := spoolPath()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	forged := `{"e":"session.end","u":"00000000-0000-4000-8000-000000000001","d":"2026-09-26","s":2,"v":"9.9.9","a":"human","sf":"tui","l":"full","p":{"tool":"claude","path":"/Users/x"}}`
	_, _ = f.WriteString(forged + "\n" + `{"e":"session.end","u":"0000`)
	_ = f.Close()
	lines := spoolLines(t)
	if len(lines) != 1 {
		t.Fatalf("read %d lines; torn or forged lines must be skipped", len(lines))
	}
}

func TestSpoolFileMode(t *testing.T) {
	c := env(t)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	p, _ := spoolPath()
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0600 {
		t.Fatalf("spool mode %v err %v", fi.Mode(), err)
	}
	sp, _ := StatePath()
	if fi, err := os.Stat(sp); err != nil || fi.Mode().Perm() != 0600 {
		t.Fatal("state mode")
	}
}

func TestTrimSpoolExpiryAndOverflow(t *testing.T) {
	now := at(20, 12, 0)
	mk := func(day time.Time, n int) []spoolLine {
		var out []spoolLine
		for i := 0; i < n; i++ {
			out = append(out, spoolLine{E: "session.end", U: newUUID(), D: dayOf(day), V: "9.9.9", A: "human", SF: "tui", L: "full",
				P: map[string]any{"tool": "claude"}})
		}
		return out
	}
	lines := append(mk(at(0, 12, 0), 10), mk(at(6, 12, 0), 10)...)
	got := trimSpool(lines, now)
	if len(got) != 10 || got[0].D != dayOf(at(6, 12, 0)) {
		t.Fatalf("expiry kept %d lines", len(got))
	}
	big := mk(at(19, 12, 0), maxSpoolLines+1000)
	got = trimSpool(big, now)
	if want := len(big) - len(big)*8/10; len(got) != len(big)-want || got[len(got)-1].U != big[len(big)-1].U {
		t.Fatalf("overflow kept %d of %d; must keep the newest 80%%", len(got), len(big))
	}
}

// TestRecordEnforcesRetentionWithoutUploader: with no project key nothing
// ever uploads, yet recording drops expired days and keeps the cap.
func TestRecordEnforcesRetentionWithoutUploader(t *testing.T) {
	c := env(t)
	grant(t, c)
	if Configured() {
		t.Fatal("setup: a key is configured")
	}
	for d := 0; d <= spoolExpiryDays+1; d++ {
		c.set(at(d, 10, 0))
		SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	}
	lines := spoolLines(t)
	cutoff := dayOf(at(spoolExpiryDays+1, 10, 0).AddDate(0, 0, -spoolExpiryDays))
	for _, l := range lines {
		if l.D < cutoff {
			t.Fatalf("day %s is older than %d days and still spooled", l.D, spoolExpiryDays)
		}
	}
	if len(lines) == 0 {
		t.Fatal("the latest days were dropped too")
	}

	var big []spoolLine
	for i := 0; i < 3000; i++ {
		h, w := 9, 6
		big = append(big, spoolLine{E: "session.end", U: newUUID(), D: dayOf(c.now()), H: &h, W: &w, S: i + 1, V: "9.9.9",
			A: "human", SF: "tui", L: "full", P: map[string]any{"tool": "claude", "end_kind": "stop"}})
	}
	if err := writeSpool(big); err != nil {
		t.Fatal(err)
	}
	if n := len(spoolBytes(t)); n <= maxSpoolBytes {
		t.Fatalf("setup: spool is only %d bytes", n)
	}
	SessionEnded(SessionEndInfo{Tool: "codex", Kind: EndStop})
	if n := len(spoolBytes(t)); n > maxSpoolBytes {
		t.Fatalf("spool is %d bytes after recording, over the %d cap", n, maxSpoolBytes)
	}
	if got := spoolLines(t); got[len(got)-1].P["tool"] != "codex" {
		t.Fatal("the new event was not appended after the trim")
	}
}

func TestSpoolLineOverOneKiBIsRefused(t *testing.T) {
	env(t)
	long := spoolLine{E: "session.end", U: newUUID(), D: "2026-09-26", V: "9.9.9", A: "human", SF: "tui", L: "full",
		P: map[string]any{"pad": string(bytes.Repeat([]byte("x"), 2000))}}
	if appendSpool(long) == nil {
		t.Fatal("a line over 1 KiB was spooled")
	}
}

func TestOffDeletesSpoolAndIdentity(t *testing.T) {
	c := env(t)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	if err := Disable("9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	s := LoadState()
	if len(spoolBytes(t)) != 0 || s.InstallID != "" || s.Salt != "" || s.Daily != nil || s.Consent != ConsentDeclined {
		t.Fatal("off must delete the spool, id, salt and counters")
	}
}

// TestSpoolHelperProcess is the child of TestConcurrentAppendsFromProcesses.
func TestSpoolHelperProcess(t *testing.T) {
	if os.Getenv("TELEMETRY_SPOOL_HELPER") != "1" {
		t.Skip("helper process")
	}
	EnableForTest(t)
	home := os.Getenv("TELEMETRY_SPOOL_HOME")
	for k, v := range map[string]string{"HOME": home, "XDG_DATA_HOME": home + "/data", "XDG_CONFIG_HOME": home + "/config", "XDG_CACHE_HOME": home + "/cache"} {
		t.Setenv(k, v)
	}
	isTerminalFn = func() bool { return true }
	SetProcess("9.9.9", SurfaceCLI)
	n, _ := strconv.Atoi(os.Getenv("TELEMETRY_SPOOL_N"))
	for i := 0; i < n; i++ {
		SessionEnded(SessionEndInfo{Tool: "codex", Kind: EndStop})
	}
}

func TestConcurrentAppendsFromProcesses(t *testing.T) {
	c := env(t)
	grant(t, c)
	const procs, each = 8, 5
	var cmds []*exec.Cmd
	for i := 0; i < procs; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSpoolHelperProcess$", "-test.count=1")
		cmd.Env = append(os.Environ(), "TELEMETRY_SPOOL_HELPER=1", "TELEMETRY_SPOOL_N="+strconv.Itoa(each),
			"TELEMETRY_SPOOL_HOME="+os.Getenv("HOME"))
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	for _, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper: %v", err)
		}
	}
	raw := bytes.Split(bytes.TrimSpace(spoolBytes(t)), []byte("\n"))
	lines := spoolLines(t)
	if len(lines) == 0 || len(lines) != len(raw) || len(lines) > procs*each {
		t.Fatalf("%d valid of %d raw lines (max %d): concurrent appends corrupted the spool", len(lines), len(raw), procs*each)
	}
	seqs := map[int]bool{}
	for _, l := range lines {
		if seqs[l.S] {
			t.Fatalf("duplicate seq %d", l.S)
		}
		seqs[l.S] = true
	}
	var s State
	sp, _ := StatePath()
	data, _ := os.ReadFile(sp)
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("state corrupted: %v", err)
	}
	var emitted int
	for _, r := range s.Daily {
		emitted += r.Emitted
	}
	if emitted != len(lines) {
		t.Fatalf("state counts %d emitted, spool has %d", emitted, len(lines))
	}
}
