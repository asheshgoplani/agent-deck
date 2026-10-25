package ui

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/ask"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// askFakeVaultScript stands in for /usr/local/bin/deck-secret (same shape as
// the internal/ask test vault): one file per name under the store dir baked
// into the script, "stored <name> (replaced=..)" on stderr.
const askFakeVaultScript = `#!/bin/sh
store='%s'
n=$(printf '%%s' "$2" | tr '/' '_')
case "$1" in
exists)
	if [ -f "$store/$n" ]; then echo yes; exit 0; fi
	echo no; exit 3 ;;
set)
	existed=no
	[ -f "$store/$n" ] && existed=yes
	cat > "$store/$n"
	echo "stored $2 (replaced=$existed)" >&2
	exit 0 ;;
esac
exit 2
`

type askTestEnv struct {
	home    string
	store   string
	profile string
}

// newAskTestEnv gives the test its own HOME (profile, ask dir, events, logs)
// and a fake vault swapped in with ask.SetVaultForTest. Not parallel-safe.
func newAskTestEnv(t *testing.T) *askTestEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(k, "")
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	store := t.TempDir()
	script := filepath.Join(t.TempDir(), "deck-secret")
	if err := os.WriteFile(script, []byte(fmt.Sprintf(askFakeVaultScript, store)), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ask.SetVaultForTest(script))
	return &askTestEnv{home: home, store: store, profile: "asktest"}
}

func (e *askTestEnv) stored(name string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(e.store, strings.ReplaceAll(name, "/", "_")))
	if err != nil {
		return "", false
	}
	return string(b), true
}

type askReqOpt func(*ask.NewRequest)

func withSession(id string) askReqOpt { return func(r *ask.NewRequest) { r.SessionID = id } }
func withReplace() askReqOpt          { return func(r *ask.NewRequest) { r.Replace = true } }
func unverified() askReqOpt           { return func(r *ask.NewRequest) { r.CallerVerified = false } }

func (e *askTestEnv) secret(t *testing.T, name string, opts ...askReqOpt) ask.Record {
	t.Helper()
	req := ask.NewRequest{
		Kind: ask.KindSecret, SessionID: "sess-1", SessionTitle: "conductor", Host: "deckbox",
		Name: name, Reason: "GitHub key with workflow permission", CallerVerified: true, Wait: 10 * time.Minute,
	}
	for _, o := range opts {
		o(&req)
	}
	rec, err := ask.Create(e.profile, req)
	if err != nil {
		t.Fatalf("ask.Create: %v", err)
	}
	return rec
}

func (e *askTestEnv) choice(t *testing.T, question string, options ...string) ask.Record {
	t.Helper()
	rec, err := ask.Create(e.profile, ask.NewRequest{
		Kind: ask.KindChoice, SessionID: "sess-1", SessionTitle: "conductor", Host: "deckbox",
		Question: question, Options: options, CallerVerified: true, Wait: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("ask.Create: %v", err)
	}
	return rec
}

func (e *askTestEnv) text(t *testing.T, question string) ask.Record {
	t.Helper()
	rec, err := ask.Create(e.profile, ask.NewRequest{
		Kind: ask.KindText, SessionID: "sess-1", SessionTitle: "conductor", Host: "deckbox",
		Question: question, CallerVerified: true, Wait: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("ask.Create: %v", err)
	}
	return rec
}

// askClock is a settable clock for the dialog (the core keeps real time).
type askClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *askClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *askClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// recordingAPI wraps the real in-process ask API and records every call the
// dialog makes (headers and values as the dialog passed them).
type recordingAPI struct {
	mu        sync.Mutex
	headers   []ask.Header
	cancels   []string
	marks     []string
	delivered int
}

func (r *recordingAPI) api() askAPI {
	return askAPI{
		list: ask.List,
		show: ask.Show,
		markSeen: func(p, id, by string) (string, error) {
			r.mu.Lock()
			r.marks = append(r.marks, id+"/"+by)
			r.mu.Unlock()
			return ask.MarkSeen(p, id, by)
		},
		deliver: func(p string, h ask.Header, v []byte) (ask.Result, error) {
			r.mu.Lock()
			r.headers = append(r.headers, h)
			r.delivered++
			r.mu.Unlock()
			return ask.Deliver(p, h, v)
		},
		cancel: func(p, id, by, nonce string) (ask.Result, error) {
			r.mu.Lock()
			r.cancels = append(r.cancels, id+"/"+by+"/"+nonce)
			r.mu.Unlock()
			return ask.Cancel(p, id, by, nonce)
		},
	}
}

func newTestAskDialog(e *askTestEnv, clk *askClock, rec *recordingAPI) *AskDialog {
	d := NewAskDialog(e.profile)
	d.now = clk.now
	if rec != nil {
		d.api = rec.api()
	}
	d.SetSize(100, 40)
	return d
}

// pollAsk runs one poll synchronously and feeds the result back.
func pollAsk(t *testing.T, d *AskDialog, canShow bool) {
	t.Helper()
	d.lastPoll = time.Time{} // the throttle is covered by TestAskHomeTickPolls
	cmd := d.PollCmd(d.now())
	if cmd == nil {
		t.Fatal("PollCmd returned nil")
	}
	msg := cmd()
	if seen := d.HandleMsg(msg, func() bool { return canShow }); seen != nil {
		// The only command a poll returns is MarkSeen (no timers).
		next := seen()
		if tick := d.HandleMsg(next, func() bool { return canShow }); tick == nil {
			t.Fatal("after MarkSeen the dialog should schedule its countdown tick")
		}
	}
}

func askKey(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func askType(d *AskDialog, s string) {
	for _, r := range s {
		d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func plain(s string) string { return ansi.Strip(s) }

func randomSentinel(t *testing.T) string {
	t.Helper()
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "ASKSENTINEL" + hex.EncodeToString(b[:])
}

func openSecretDialog(t *testing.T, opts ...askReqOpt) (*askTestEnv, *AskDialog, *recordingAPI, *askClock, ask.Record) {
	t.Helper()
	e := newAskTestEnv(t)
	rec := e.secret(t, "conductor/GH_TOKEN_DECKBOX", opts...)
	clk := &askClock{t: rec.RequestedAt.Add(4 * time.Second)}
	api := &recordingAPI{}
	d := newTestAskDialog(e, clk, api)
	pollAsk(t, d, true)
	if !d.IsVisible() {
		t.Fatal("modal not shown for a pending secret request older than 3 s")
	}
	return e, d, api, clk, rec
}

func TestAskDialogSecretRendersFacts(t *testing.T) {
	_, d, api, _, rec := openSecretDialog(t)
	v := plain(d.View())
	for _, want := range []string{
		"conductor asks for a secret", "deckbox", "conductor", "asktest", "conductor/GH_TOKEN_DECKBOX",
		"The agent says:", "GitHub key with workflow permission", "Expires in", "9m56s", "Save", "Cancel",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	for _, not := range []string{"REPLACES an existing secret", "Unverified caller"} {
		if strings.Contains(v, not) {
			t.Errorf("view shows %q for a verified, non-replacing request", not)
		}
	}
	if len(api.marks) != 1 || api.marks[0] != rec.RequestID+"/tui" {
		t.Errorf("MarkSeen calls = %v, want one by tui", api.marks)
	}
	got, err := ask.Show(rec.Profile, rec.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SeenBy) != 1 || got.SeenBy[0] != "tui" {
		t.Errorf("record seen_by = %v, want [tui]", got.SeenBy)
	}
	if d.input.EchoMode != textinput.EchoPassword || d.input.EchoCharacter != '•' {
		t.Errorf("secret field echo = %v %q, want EchoPassword '•'", d.input.EchoMode, d.input.EchoCharacter)
	}
}

func TestAskDialogSecretWarningRows(t *testing.T) {
	e := newAskTestEnv(t)
	if err := os.WriteFile(filepath.Join(e.store, "conductor_GH_TOKEN_DECKBOX"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := e.secret(t, "conductor/GH_TOKEN_DECKBOX", withReplace(), unverified())
	d := newTestAskDialog(e, &askClock{t: rec.RequestedAt.Add(4 * time.Second)}, nil)
	pollAsk(t, d, true)
	v := plain(d.View())
	for _, want := range []string{"REPLACES an existing secret", "Unverified caller (outside any agent-deck session)"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
}

func TestAskDialogReasonSanitizedAtMostTwoLines(t *testing.T) {
	e := newAskTestEnv(t)
	d := newTestAskDialog(e, &askClock{t: time.Now()}, nil)
	long := "first\u202e line\nsecond\u200b line " + strings.Repeat("word ", 60)
	d.open(ask.Record{
		RequestID: "0123456789abcdef", Kind: ask.KindSecret, SessionID: "s", SessionTitle: "t\u202ex",
		Host: "deckbox", Profile: "asktest", Name: "conductor/X", Reason: long, CallerVerified: true,
		RequestedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute), State: ask.StatePending,
	})
	v := plain(d.View())
	if strings.ContainsAny(v, "\u202e\u200b") {
		t.Error("bidi / zero-width characters reached the view")
	}
	lines := strings.Split(v, "\n")
	start := -1
	for i, l := range lines {
		if strings.Contains(l, "The agent says:") {
			start = i
		}
	}
	if start < 0 {
		t.Fatalf("no reason label:\n%s", v)
	}
	var reasonLines int
	for _, l := range lines[start+1:] {
		if strings.Contains(l, "Expires in") {
			break
		}
		if strings.TrimSpace(strings.Trim(l, "│ ")) != "" {
			reasonLines++
		}
	}
	if reasonLines != 2 {
		t.Errorf("reason renders on %d lines, want 2 (capped):\n%s", reasonLines, v)
	}
	if !strings.Contains(v, "…") {
		t.Error("a capped reason should end with an ellipsis")
	}
}

func TestAskDialogTypedSecretRendersOnlyAsBullets(t *testing.T) {
	_, d, _, _, _ := openSecretDialog(t)
	sentinel := randomSentinel(t)
	askType(d, sentinel)
	v := plain(d.View())
	if strings.Contains(v, sentinel) || strings.Contains(v, sentinel[:6]) || strings.Contains(v, sentinel[len(sentinel)-6:]) {
		t.Fatalf("typed value visible in the view:\n%s", v)
	}
	if n := strings.Count(v, "•"); n < len(sentinel) {
		t.Errorf("view shows %d bullets, want at least %d", n, len(sentinel))
	}
	// No show toggle: keys that toggle in other UIs leave the field masked.
	for _, k := range []string{"tab", "ctrl+c"} {
		if k == "ctrl+c" {
			break // ctrl+c cancels; covered by the cancel tests
		}
		d.Update(askKey(k))
	}
	if d.input.EchoMode != textinput.EchoPassword {
		t.Fatal("echo mode changed: the TUI modal must have no show toggle")
	}
	if v := plain(d.View()); strings.Contains(v, sentinel) || strings.Contains(strings.ToLower(v), "show") {
		t.Fatalf("view offers a show toggle or the value:\n%s", v)
	}
}

func TestAskDialogButtonsSameWidth(t *testing.T) {
	_, d, _, _, _ := openSecretDialog(t)
	save, cancel := d.buttons()
	if lipgloss.Width(save) != lipgloss.Width(cancel) {
		t.Errorf("Save width %d != Cancel width %d", lipgloss.Width(save), lipgloss.Width(cancel))
	}
	askType(d, "x")
	save, cancel = d.buttons()
	if lipgloss.Width(save) != lipgloss.Width(cancel) {
		t.Errorf("enabled Save width %d != Cancel width %d", lipgloss.Width(save), lipgloss.Width(cancel))
	}
}

func TestAskDialogEnterWithEmptyDoesNothing(t *testing.T) {
	e, d, api, _, rec := openSecretDialog(t)
	if cmd := d.Update(askKey("enter")); cmd != nil {
		t.Fatal("Enter on an empty field returned a command")
	}
	if !d.IsVisible() || d.phase != askPhaseEditing {
		t.Fatalf("Enter on an empty field changed the dialog (visible=%v phase=%v)", d.IsVisible(), d.phase)
	}
	if api.delivered != 0 {
		t.Fatal("Deliver called for an empty field")
	}
	if _, ok := e.stored(rec.Name); ok {
		t.Fatal("vault written for an empty field")
	}
	if got, _ := ask.Show(rec.Profile, rec.RequestID); got.State != ask.StatePending {
		t.Fatalf("state = %s, want pending", got.State)
	}
}

func TestAskDialogEscCancels(t *testing.T) {
	_, d, api, _, rec := openSecretDialog(t)
	askType(d, "partial")
	cmd := d.Update(askKey("esc"))
	if cmd == nil {
		t.Fatal("Esc returned no cancel command")
	}
	if d.IsVisible() {
		t.Error("modal still visible after Esc")
	}
	if d.input.Value() != "" {
		t.Error("input not reset on cancel")
	}
	d.HandleMsg(cmd(), func() bool { return true })
	got, err := ask.Show(rec.Profile, rec.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != ask.StateCancelled || got.AnsweredBy != "tui" {
		t.Fatalf("record = %s by %q, want cancelled by tui", got.State, got.AnsweredBy)
	}
	if len(api.cancels) != 1 || !strings.HasPrefix(api.cancels[0], rec.RequestID+"/tui/") || strings.HasSuffix(api.cancels[0], "/") {
		t.Fatalf("cancel calls = %v, want one by tui with the MarkSeen nonce", api.cancels)
	}
	if api.delivered != 0 {
		t.Fatal("Esc delivered")
	}
}

func TestAskDialogSaveDeliversWithMatchingHeader(t *testing.T) {
	e, d, api, _, rec := openSecretDialog(t)
	sentinel := randomSentinel(t)
	askType(d, sentinel)
	cmd := d.Update(askKey("enter"))
	if cmd == nil {
		t.Fatal("Enter with a value returned no deliver command")
	}
	if d.input.Value() != "" {
		t.Fatal("input still holds the value after Save: it must be reset before Deliver runs")
	}
	if d.phase != askPhaseSaving {
		t.Fatalf("phase = %v, want saving", d.phase)
	}
	d.HandleMsg(cmd(), func() bool { return true })
	if len(api.headers) != 1 {
		t.Fatalf("Deliver calls = %d, want 1", len(api.headers))
	}
	h := api.headers[0]
	want := ask.Header{RequestID: rec.RequestID, Kind: ask.KindSecret, Name: rec.Name, Replace: false, Nonce: h.Nonce, AnsweredBy: "tui"}
	if h != want {
		t.Fatalf("header = %+v, want %+v", h, want)
	}
	if len(h.Nonce) != 16 || h.Nonce != d.nonce {
		t.Fatalf("header nonce %q is not the MarkSeen nonce %q", h.Nonce, d.nonce)
	}
	if v, ok := e.stored(rec.Name); !ok || v != sentinel {
		t.Fatal("fake vault did not receive the typed value")
	}
	got, _ := ask.Show(rec.Profile, rec.RequestID)
	if got.State != ask.StateStored || got.AnsweredBy != "tui" {
		t.Fatalf("record = %s by %q, want stored by tui", got.State, got.AnsweredBy)
	}
	v := plain(d.View())
	if !strings.Contains(v, "Stored conductor/GH_TOKEN_DECKBOX") || strings.Contains(v, sentinel) {
		t.Fatalf("stored view wrong:\n%s", v)
	}
	d.HandleMsg(askCloseMsg{id: rec.RequestID}, func() bool { return true })
	if d.IsVisible() {
		t.Fatal("stored confirmation did not close")
	}
}

func TestAskDialogDeliverRefusalKeepsValueOut(t *testing.T) {
	_, d, _, _, rec := openSecretDialog(t)
	d.api.deliver = func(string, ask.Header, []byte) (ask.Result, error) { return ask.Result{}, ask.ErrVaultRefused }
	askType(d, "abc")
	d.HandleMsg(d.Update(askKey("enter"))(), func() bool { return true })
	v := plain(d.View())
	if !strings.Contains(v, "vault refused") {
		t.Fatalf("refusal not shown:\n%s", v)
	}
	if d.phase != askPhaseEditing || d.input.Value() != "" {
		t.Fatalf("after a retryable refusal: phase=%v input empty=%v", d.phase, d.input.Value() == "")
	}
	_ = rec
}

func TestAskDialogAnsweredElsewhere(t *testing.T) {
	e, d, api, _, rec := openSecretDialog(t)
	askType(d, "typed-here")
	appNonce, err := ask.MarkSeen(e.profile, rec.RequestID, "app")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ask.Deliver(e.profile, ask.Header{RequestID: rec.RequestID, Kind: rec.Kind, Name: rec.Name, Nonce: appNonce, AnsweredBy: "app"}, []byte("from-the-app")); err != nil {
		t.Fatal(err)
	}
	pollAsk(t, d, true)
	if !d.IsVisible() {
		t.Fatal("modal closed quietly when the request was answered elsewhere")
	}
	v := plain(d.View())
	if !strings.Contains(v, "Answered elsewhere (app), not by you") {
		t.Fatalf("view lacks the answered-elsewhere line:\n%s", v)
	}
	if d.input.Value() != "" {
		t.Error("input kept its value after the request was answered elsewhere")
	}
	if cmd := d.Update(askKey("enter")); cmd != nil {
		t.Error("Enter in the answered-elsewhere state returned a command")
	}
	if d.IsVisible() {
		t.Error("a key should dismiss the answered-elsewhere notice")
	}
	if api.delivered != 0 || len(api.cancels) != 0 {
		t.Fatalf("dialog called deliver/cancel (%d/%d) for a request answered elsewhere", api.delivered, len(api.cancels))
	}
}

func TestAskDialogCooldownAfterCancel(t *testing.T) {
	e, d, _, clk, _ := openSecretDialog(t)
	d.HandleMsg(d.Update(askKey("esc"))(), func() bool { return true })
	cancelledAt := clk.now()
	second := e.secret(t, "conductor/OTHER")
	clk.set(maxTime(cancelledAt, second.RequestedAt.Add(4*time.Second)).Add(10 * time.Second))
	pollAsk(t, d, true)
	if d.IsVisible() {
		t.Fatal("a new request from the same session showed within 30 s of a Cancel")
	}
	// Another session is not held back by that cooldown.
	other := e.secret(t, "conductor/THIRD", withSession("sess-2"))
	clk.set(maxTime(clk.now(), other.RequestedAt.Add(4*time.Second)))
	pollAsk(t, d, true)
	if !d.IsVisible() || d.rec.RequestID != other.RequestID {
		t.Fatal("a request from another session should show during the first session's cooldown")
	}
	d.HandleMsg(d.Update(askKey("esc"))(), func() bool { return true })
	clk.set(cancelledAt.Add(31 * time.Second))
	pollAsk(t, d, true)
	if !d.IsVisible() || d.rec.RequestID != second.RequestID {
		t.Fatal("the request should show once the 30 s cooldown has passed")
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func TestAskDialogWaitsThreeSecondsAndSkipsAppSeen(t *testing.T) {
	e := newAskTestEnv(t)
	first := e.secret(t, "conductor/A")
	clk := &askClock{t: first.RequestedAt.Add(time.Second)}
	d := newTestAskDialog(e, clk, nil)
	pollAsk(t, d, true)
	if d.IsVisible() {
		t.Fatal("shown before the app had 3 s to mark it seen")
	}
	if _, err := ask.MarkSeen(e.profile, first.RequestID, "app"); err != nil {
		t.Fatal(err)
	}
	clk.set(first.RequestedAt.Add(4 * time.Second))
	pollAsk(t, d, true)
	if d.IsVisible() {
		t.Fatal("shown although the app marked it seen")
	}
	second := e.secret(t, "conductor/B", withSession("sess-2"))
	clk.set(second.RequestedAt.Add(4 * time.Second))
	pollAsk(t, d, false)
	if d.IsVisible() {
		t.Fatal("shown while the home said it cannot show a modal")
	}
	pollAsk(t, d, true)
	if !d.IsVisible() || d.rec.RequestID != second.RequestID {
		t.Fatal("the request nobody showed should open")
	}
}

func TestAskDialogOneModalAtATimeOldestFirst(t *testing.T) {
	e := newAskTestEnv(t)
	first := e.secret(t, "conductor/A")
	time.Sleep(5 * time.Millisecond)
	second := e.secret(t, "conductor/B", withSession("sess-2"))
	clk := &askClock{t: second.RequestedAt.Add(4 * time.Second)}
	d := newTestAskDialog(e, clk, nil)
	pollAsk(t, d, true)
	if d.rec.RequestID != first.RequestID {
		t.Fatal("not the oldest pending request")
	}
	pollAsk(t, d, true)
	if d.rec.RequestID != first.RequestID {
		t.Fatal("a second request replaced the open modal")
	}
	if !strings.Contains(plain(d.View()), "1 more waiting") {
		t.Errorf("queue hint missing:\n%s", plain(d.View()))
	}
}

func TestAskDialogChoice(t *testing.T) {
	e := newAskTestEnv(t)
	rec := e.choice(t, "Which branch?", "main", "release")
	api := &recordingAPI{}
	d := newTestAskDialog(e, &askClock{t: rec.RequestedAt.Add(4 * time.Second)}, api)
	pollAsk(t, d, true)
	v := plain(d.View())
	for _, want := range []string{"conductor asks you to choose", "Which branch?", "main", "release", "Cancel"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	d.Update(askKey("down"))
	cmd := d.Update(askKey("enter"))
	if cmd == nil {
		t.Fatal("Enter on an option returned no command")
	}
	msg := cmd()
	d.HandleMsg(msg, func() bool { return true })
	if len(api.headers) != 1 || api.headers[0].Kind != ask.KindChoice || api.headers[0].Nonce == "" {
		t.Fatalf("choice headers = %+v", api.headers)
	}
	if dm, ok := msg.(askDeliveredMsg); !ok || dm.err != nil || dm.res.Status != ask.StateAnswered {
		t.Fatalf("deliver result = %+v", msg)
	}
}

func TestAskDialogChoiceCancelItem(t *testing.T) {
	e := newAskTestEnv(t)
	rec := e.choice(t, "Which branch?", "main", "release")
	api := &recordingAPI{}
	d := newTestAskDialog(e, &askClock{t: rec.RequestedAt.Add(4 * time.Second)}, api)
	pollAsk(t, d, true)
	d.Update(askKey("down"))
	d.Update(askKey("down"))
	cmd := d.Update(askKey("enter"))
	if cmd == nil || d.IsVisible() {
		t.Fatal("Enter on Cancel should cancel and close")
	}
	d.HandleMsg(cmd(), func() bool { return true })
	if got, _ := ask.Show(e.profile, rec.RequestID); got.State != ask.StateCancelled {
		t.Fatalf("state = %s, want cancelled", got.State)
	}
	if api.delivered != 0 {
		t.Fatal("Cancel item delivered an answer")
	}
}

func TestAskDialogText(t *testing.T) {
	e := newAskTestEnv(t)
	rec := e.text(t, "Favourite colour?")
	api := &recordingAPI{}
	d := newTestAskDialog(e, &askClock{t: rec.RequestedAt.Add(4 * time.Second)}, api)
	pollAsk(t, d, true)
	v := plain(d.View())
	for _, want := range []string{"Favourite colour?", "Your answer goes to the agent and its transcript. Never type a secret here."} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if d.input.EchoMode != textinput.EchoNormal {
		t.Error("text answers use a plain input")
	}
	if cmd := d.Update(askKey("enter")); cmd != nil {
		t.Fatal("Enter on an empty text answer returned a command")
	}
	askType(d, "blue")
	d.HandleMsg(d.Update(askKey("enter"))(), func() bool { return true })
	if got, _ := ask.Show(e.profile, rec.RequestID); got.State != ask.StateAnswered || got.AnsweredBy != "tui" {
		t.Fatalf("record = %s by %q, want answered by tui", got.State, got.AnsweredBy)
	}
}

func TestAskDialogTextCredentialRefused(t *testing.T) {
	e := newAskTestEnv(t)
	rec := e.text(t, "Paste it")
	d := newTestAskDialog(e, &askClock{t: rec.RequestedAt.Add(4 * time.Second)}, nil)
	pollAsk(t, d, true)
	askType(d, "ghp_"+strings.Repeat("a1B2", 8))
	d.HandleMsg(d.Update(askKey("enter"))(), func() bool { return true })
	v := plain(d.View())
	if !strings.Contains(v, "looks like a secret") || strings.Contains(v, "ghp_") {
		t.Fatalf("credential refusal view wrong:\n%s", v)
	}
	if got, _ := ask.Show(e.profile, rec.RequestID); got.State != ask.StatePending {
		t.Fatalf("state = %s, want pending", got.State)
	}
}

func TestAskDialogCountdownAndExpiry(t *testing.T) {
	_, d, api, clk, rec := openSecretDialog(t)
	clk.set(rec.ExpiresAt.Add(-75 * time.Second))
	d.HandleMsg(askTickMsg{id: rec.RequestID}, func() bool { return true })
	if v := plain(d.View()); !strings.Contains(v, "Expires in 1m15s") {
		t.Fatalf("countdown wrong:\n%s", v)
	}
	askType(d, "late")
	clk.set(rec.ExpiresAt.Add(time.Second))
	d.HandleMsg(askTickMsg{id: rec.RequestID}, func() bool { return true })
	v := plain(d.View())
	if !strings.Contains(v, "Expired") || d.input.Value() != "" {
		t.Fatalf("expiry not shown or input kept:\n%s", v)
	}
	if cmd := d.Update(askKey("enter")); cmd != nil || api.delivered != 0 {
		t.Fatal("Enter after expiry delivered")
	}
}

func TestAskDialogIgnoresOtherProfiles(t *testing.T) {
	e := newAskTestEnv(t)
	other := *e
	other.profile = "someoneelse"
	rec := other.secret(t, "conductor/A")
	d := newTestAskDialog(e, &askClock{t: rec.RequestedAt.Add(4 * time.Second)}, nil)
	pollAsk(t, d, true)
	if d.IsVisible() {
		t.Fatal("modal shown for a request of another profile")
	}
}

func TestAskDialogDeliverErrorNeverShowsInternalText(t *testing.T) {
	_, d, _, _, _ := openSecretDialog(t)
	d.api.deliver = func(string, ask.Header, []byte) (ask.Result, error) {
		return ask.Result{}, errors.New("internal detail /some/path")
	}
	askType(d, "abc")
	d.HandleMsg(d.Update(askKey("enter"))(), func() bool { return true })
	if v := plain(d.View()); strings.Contains(v, "/some/path") || !strings.Contains(v, "could not save") {
		t.Fatalf("non-refusal error view:\n%s", v)
	}
}

// Security review 2: an app seen (forged over the ungated events publish, or
// a dialog that never answers) hides the request from the TUI only for
// ask.SeenHold; with no deliver or cancel by then the TUI shows it anyway.
func TestAskDialogShowsRequestOnceTheAppSeenIsStale(t *testing.T) {
	e := newAskTestEnv(t)
	rec := e.secret(t, "conductor/A")
	if _, err := ask.MarkSeen(e.profile, rec.RequestID, "app"); err != nil {
		t.Fatal(err)
	}
	shown, err := ask.Show(e.profile, rec.RequestID)
	if err != nil || len(shown.Seen) != 1 {
		t.Fatalf("Show = %+v %v", shown, err)
	}
	seenAt := shown.Seen[0].At
	clk := &askClock{t: seenAt.Add(ask.SeenHold - time.Second)}
	d := newTestAskDialog(e, clk, nil)
	pollAsk(t, d, true)
	if d.IsVisible() {
		t.Fatal("shown while the app's seen is fresh")
	}
	clk.set(seenAt.Add(ask.SeenHold + time.Second))
	pollAsk(t, d, true)
	if !d.IsVisible() || d.rec.RequestID != rec.RequestID {
		t.Fatal("an app seen with no deliver or cancel for ask.SeenHold still hides the request from the TUI")
	}
}
