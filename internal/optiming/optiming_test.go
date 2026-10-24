package optiming

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// useDir points the timings file at a temp dir and restores state after.
func useDir(t *testing.T, on bool) string {
	t.Helper()
	dir := t.TempDir()
	prevDir, prevOn := dirFunc, enabled.Load()
	Close()
	dirFunc = func() (string, error) { return dir, nil }
	enabled.Store(on)
	t.Cleanup(func() {
		Close()
		dirFunc = prevDir
		enabled.Store(prevOn)
	})
	return dir
}

func TestOffWritesNothing(t *testing.T) {
	dir := useDir(t, false)
	Start("cli.list")()
	StartResult("cli.add")(true)
	Since("tui.attach", time.Now().Add(-time.Second))
	if _, err := os.Stat(filepath.Join(dir, FileName)); !os.IsNotExist(err) {
		t.Fatalf("timings file exists with timing off (err=%v)", err)
	}
}

func TestOnRecordsOneLinePerOperation(t *testing.T) {
	dir := useDir(t, true)
	stop := Start("cli.list")
	time.Sleep(2 * time.Millisecond)
	stop()
	StartResult("cli.add")(true)
	Since("tui.attach", time.Now().Add(-40*time.Millisecond))
	Since("tui.detach", time.Time{}) // zero start: nothing recorded

	got, err := ReadFile(filepath.Join(dir, FileName), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(got), got)
	}
	if got[0].Op != "cli.list" || got[0].MS < 2 || got[0].Fail {
		t.Errorf("entry 0 = %+v, want cli.list >= 2ms ok", got[0])
	}
	if got[1].Op != "cli.add" || !got[1].Fail {
		t.Errorf("entry 1 = %+v, want cli.add failed", got[1])
	}
	if got[2].Op != "tui.attach" || got[2].MS < 40 || got[2].PID != os.Getpid() {
		t.Errorf("entry 2 = %+v, want tui.attach >= 40ms with our pid", got[2])
	}
}

func TestEnvSwitch(t *testing.T) {
	for v, want := range map[string]bool{"1": true, "true": true, " ON ": true, "yes": true, "": false, "0": false, "log": false} {
		if got := envOn(v); got != want {
			t.Errorf("envOn(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestRotatesFullFile(t *testing.T) {
	dir := useDir(t, true)
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, make([]byte, maxFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	Start("cli.list")()
	fi, err := os.Stat(path)
	if err != nil || fi.Size() > 1024 {
		t.Fatalf("after rotation timings file size = %v (err %v), want one short line", fi.Size(), err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated file missing: %v", err)
	}
}

func TestSummarizeAgainstTargets(t *testing.T) {
	now := time.Now()
	var es []Entry
	for i := 1; i <= 20; i++ {
		es = append(es, Entry{Time: now, Op: "cli.events.publish", MS: float64(1000 + i)})
		es = append(es, Entry{Time: now, Op: "cli.list", MS: float64(i), Fail: i == 3})
	}
	es = append(es, Entry{Time: now, Op: "custom.op", MS: 5})
	sum := Summarize(es)
	if len(sum) != 3 || sum[0].Op != "cli.events.publish" {
		t.Fatalf("summary order = %+v, want events.publish (most over target) first", sum)
	}
	if !sum[0].Over || sum[0].Target != 100 || sum[0].P95MS != 1019 || sum[0].P50MS != 1010 {
		t.Errorf("events.publish = %+v, want over target 100, p50 1010, p95 1019", sum[0])
	}
	var list Summary
	for _, s := range sum {
		if s.Op == "cli.list" {
			list = s
		}
	}
	if list.Over || list.Fails != 1 || list.Count != 20 || list.MaxMS != 20 {
		t.Errorf("cli.list = %+v, want 20 runs, 1 fail, under target", list)
	}
}

func TestTargetForPrefixFallback(t *testing.T) {
	if tg, ok := TargetFor("cli.session.fork"); !ok || tg.Op != "cli." {
		t.Errorf("cli.session.fork -> %+v %v, want the cli. default", tg, ok)
	}
	if tg, ok := TargetFor("cli.session.send"); !ok || tg.Op != "cli.session.send" {
		t.Errorf("cli.session.send -> %+v, want its own entry", tg)
	}
	if _, ok := TargetFor("nothing.here"); ok {
		t.Error("unknown op got a target")
	}
}

func TestReadFileSkipsOldAndMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	old := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339Nano)
	recent := time.Now().UTC().Format(time.RFC3339Nano)
	body := `{"t":"` + old + `","op":"cli.list","ms":1}` + "\nnot json\n" + `{"t":"` + recent + `","op":"cli.list","ms":2}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path, time.Now().Add(-24*time.Hour))
	if err != nil || len(got) != 1 || got[0].MS != 2 {
		t.Fatalf("ReadFile = %+v, %v; want only the recent entry", got, err)
	}
}

// BenchmarkOp_TimingHookOff is the hot-path cost of a hook with timing off: it must
// stay at zero allocations and about a nanosecond.
func BenchmarkOp_TimingHookOff(b *testing.B) {
	prev := enabled.Load()
	enabled.Store(false)
	defer enabled.Store(prev)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Start("tui.preview")()
	}
}

func BenchmarkOp_TimingSinceOff(b *testing.B) {
	prev := enabled.Load()
	enabled.Store(false)
	defer enabled.Store(prev)
	start := time.Now()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		Since("tui.attach", start)
	}
}

// TestOffHookAllocatesNothing pins the hot-path promise: with timing off a
// hook allocates nothing.
func TestOffHookAllocatesNothing(t *testing.T) {
	useDir(t, false)
	start := time.Now()
	if n := testing.AllocsPerRun(1000, func() {
		Start("tui.preview")()
		StartResult("cli.list")(false)
		Since("tui.attach", start)
	}); n != 0 {
		t.Fatalf("off hooks allocate %v times per run, want 0", n)
	}
}

// TestFollowsRotationByAnotherProcess: when another process rotates the file
// away, the next record goes to the new file, not the rotated copy.
func TestFollowsRotationByAnotherProcess(t *testing.T) {
	dir := useDir(t, true)
	path := filepath.Join(dir, FileName)
	Start("cli.list")()
	if err := os.Rename(path, path+".1"); err != nil { // "another process" rotates
		t.Fatal(err)
	}
	Start("cli.status")()
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), "cli.status") || strings.Contains(string(b), "cli.list") {
		t.Fatalf("new file after rotation = %q (%v), want only cli.status", b, err)
	}
}

// TestRotatesWhenAnotherProcessFilledTheFile: the size check counts every
// process's lines, not only this process's writes.
func TestRotatesWhenAnotherProcessFilledTheFile(t *testing.T) {
	dir := useDir(t, true)
	path := filepath.Join(dir, FileName)
	Start("cli.list")() // this process opens the file
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, maxFileBytes+1)); err != nil { // "another process" fills it
		t.Fatal(err)
	}
	_ = f.Close()
	Start("cli.status")()
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), "cli.status") || len(b) > 1024 {
		t.Fatalf("after another process filled the file: %d bytes (%v), want a fresh file with cli.status", len(b), err)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("full file not rotated: %v", err)
	}
}
