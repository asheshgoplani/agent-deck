package macapp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func validRequest(op Op, args BrowserArgs) BrowserRequest {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	return BrowserRequest{V: ProtocolVersion, ReqID: "0123456789abcdef0123456789abcdef", Op: op, Args: args, Host: "box", RequestedAt: now, ExpiresAt: now.Add(30 * time.Second)}
}

// The frame data uses the snake_case names of the contract and nothing else.
func TestBrowserRequestWireShape(t *testing.T) {
	raw, err := json.Marshal(validRequest(OpOpen, BrowserArgs{URL: "https://example.com/", NewTab: true}))
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range data {
		keys = append(keys, k)
	}
	for _, want := range []string{"v", "req_id", "op", "args", "host", "requested_at", "expires_at"} {
		if _, ok := data[want]; !ok {
			t.Fatalf("missing %q in %s", want, raw)
		}
	}
	if len(data) != 7 {
		t.Fatalf("unexpected keys %v", keys)
	}
	if string(data["args"]) != `{"url":"https://example.com/","new_tab":true}` {
		t.Fatalf("args: %s", data["args"])
	}
	resp := BrowserResponse{V: 1, ReqID: "r1", RequestEventID: "e1", OK: false, Error: &ErrorInfo{Code: CodeOwnerDriving, Message: "m"}, Driver: DriverOwner, AppID: "a"}
	raw, err = json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"v":1,"req_id":"r1","request_event_id":"e1","ok":false,"error":{"code":"owner_driving","message":"m"},"driver":"owner","app_id":"a"}` {
		t.Fatalf("response: %s", raw)
	}
	claim := FocusClaim{AppID: "a", Mac: "m", User: "u"}
	raw, err = json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"app_id"`, `"mac"`, `"user"`, `"claimed_at"`, `"lease_until"`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("focus claim lacks %s: %s", key, raw)
		}
	}
	if KindBrowserRequest != "macapp.browser.request" || KindBrowserResponse != "macapp.browser.response" || KindAppFocus != "macapp.app.focus" || KindSessionClosed != "macapp.session.closed" {
		t.Fatal("kind strings drifted from the contract")
	}
}

func TestBrowserArgsAcceptedPerOp(t *testing.T) {
	for _, tc := range []struct {
		op   Op
		args BrowserArgs
	}{
		{OpOpen, BrowserArgs{URL: "https://example.com/x?q=1"}},
		{OpOpen, BrowserArgs{URL: "http://localhost:8080/", TabID: "t1", BrowserProfile: "work"}},
		{OpOpen, BrowserArgs{Path: "/tmp/report/index.html", NewTab: true}},
		{OpTabs, BrowserArgs{}},
		{OpClose, BrowserArgs{TabID: "t1"}},
		{OpClick, BrowserArgs{Ref: "e12"}},
		{OpClick, BrowserArgs{Selector: "#go", TabID: "t1"}},
		{OpClick, BrowserArgs{Text: "Sign in"}},
		{OpType, BrowserArgs{Ref: "e3", Text: "hello", Submit: true}},
		{OpType, BrowserArgs{Selector: "input[name=q]", Text: strings.Repeat("a", MaxTypeTextBytes)}},
		{OpRead, BrowserArgs{Mode: ReadModeText}},
		{OpRead, BrowserArgs{Mode: ReadModeOutline, TabID: "t1"}},
		{OpShot, BrowserArgs{}},
		{OpShot, BrowserArgs{FullPage: true, TabID: "t2"}},
	} {
		if err := validRequest(tc.op, tc.args).Validate(); err != nil {
			t.Errorf("%s %+v refused: %v", tc.op, tc.args, err)
		}
	}
}

func TestBrowserArgsRefusedPerOp(t *testing.T) {
	for _, tc := range []struct {
		op   Op
		args BrowserArgs
	}{
		{"eval", BrowserArgs{}},
		{"", BrowserArgs{}},
		{OpOpen, BrowserArgs{}},
		{OpOpen, BrowserArgs{URL: "https://example.com", Path: "/tmp/a.html"}},
		{OpOpen, BrowserArgs{URL: "javascript:alert(1)"}},
		{OpOpen, BrowserArgs{URL: "file:///etc/passwd"}},
		{OpOpen, BrowserArgs{URL: "https://user:secret@example.com/"}},
		{OpOpen, BrowserArgs{URL: "https://"}},
		{OpOpen, BrowserArgs{URL: "example.com"}},
		{OpOpen, BrowserArgs{URL: "https://example.com/\n"}},
		{OpOpen, BrowserArgs{Path: "relative/index.html"}},
		{OpOpen, BrowserArgs{Path: "/tmp/../etc/passwd"}},
		{OpOpen, BrowserArgs{URL: "https://example.com", TabID: "t1", NewTab: true}},
		{OpOpen, BrowserArgs{URL: "https://example.com", Text: "x"}},
		{OpTabs, BrowserArgs{TabID: "t1"}},
		{OpClose, BrowserArgs{}},
		{OpClose, BrowserArgs{TabID: "t1", FullPage: true}},
		{OpClick, BrowserArgs{}},
		{OpClick, BrowserArgs{Ref: "e1", Selector: "#a"}},
		{OpClick, BrowserArgs{Ref: "e1", Submit: true}},
		{OpType, BrowserArgs{Ref: "e1"}},
		{OpType, BrowserArgs{Text: "x"}},
		{OpType, BrowserArgs{Ref: "e1", Selector: "#a", Text: "x"}},
		{OpType, BrowserArgs{Ref: "e1", Text: strings.Repeat("a", MaxTypeTextBytes+1)}},
		{OpType, BrowserArgs{Ref: "e1\n", Text: "x"}},
		{OpRead, BrowserArgs{}},
		{OpRead, BrowserArgs{Mode: "html"}},
		{OpRead, BrowserArgs{Mode: ReadModeText, URL: "https://example.com"}},
		{OpShot, BrowserArgs{Mode: ReadModeText}},
	} {
		if err := validRequest(tc.op, tc.args).Validate(); err == nil {
			t.Errorf("%q %+v accepted", tc.op, tc.args)
		}
	}
	bad := validRequest(OpTabs, BrowserArgs{})
	bad.V = 2
	if bad.Validate() == nil {
		t.Error("unknown version accepted")
	}
	for _, id := range []string{"", " ", "r1", "0123456789ABCDEF0123456789ABCDEF", "0123456789abcdef0123456789abcde", "0123456789abcdef0123456789abcdefa", "0123456789abcdef0123456789abcdeg"} {
		bad = validRequest(OpTabs, BrowserArgs{})
		bad.ReqID = id
		if bad.Validate() == nil {
			t.Errorf("req_id %q accepted (the contract says 32 lowercase hex)", id)
		}
	}
	bad = validRequest(OpTabs, BrowserArgs{})
	bad.ExpiresAt = bad.RequestedAt
	if bad.Validate() == nil {
		t.Error("expires_at not after requested_at accepted")
	}
}

// The browser uses the open command's URL rules.
func TestValidateURL(t *testing.T) {
	for _, tc := range []struct {
		target, want string
		err          error
		bad          bool
	}{
		{"https://example.com/report?q=a%20b", "https://example.com/report?q=a%20b", nil, false},
		{"http://localhost:8080/", "http://localhost:8080/", nil, false},
		{"report/index.html", "", ErrNotURL, true},
		{"/abs/page.html", "", ErrNotURL, true},
		{"javascript:alert(1)", "", nil, true},
		{"https://user:secret@example.com", "", nil, true},
		{"https://[bad", "", nil, true},
		{"", "", nil, true},
		{"https://example.com/\r", "", nil, true},
	} {
		got, err := ValidateURL(tc.target)
		if got != tc.want || (err != nil) != tc.bad || (tc.err != nil && !errors.Is(err, tc.err)) || (tc.err == nil && errors.Is(err, ErrNotURL)) {
			t.Errorf("%q: %q %v", tc.target, got, err)
		}
	}
}

func TestDecodeReadAndShotCaps(t *testing.T) {
	read, err := DecodeRead(json.RawMessage(`{"url":"https://example.com/","title":"T","text":"hello"}`))
	if err != nil || read.Text != "hello" || read.Title != "T" {
		t.Fatalf("read: %+v %v", read, err)
	}
	big, _ := json.Marshal(ReadResult{Text: strings.Repeat("a", MaxReadTextBytes+1)})
	if _, err := DecodeRead(big); err == nil {
		t.Fatal("oversized read accepted")
	}
	image := []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3}
	raw, _ := json.Marshal(ShotResult{MIME: "image/jpeg", ImageB64: base64.StdEncoding.EncodeToString(image), Width: 10, Height: 20})
	shot, got, err := DecodeShot(raw)
	if err != nil || string(got) != string(image) || shot.Width != 10 || shot.Height != 20 {
		t.Fatalf("shot: %+v %v", shot, err)
	}
	for _, bad := range []ShotResult{
		{ImageB64: base64.StdEncoding.EncodeToString(make([]byte, MaxShotBytes+1))},
		{ImageB64: "not base64!"},
		{ImageB64: ""},
	} {
		raw, _ := json.Marshal(bad)
		if _, _, err := DecodeShot(raw); err == nil {
			t.Errorf("bad shot accepted: %d bytes of base64", len(bad.ImageB64))
		}
	}
	if _, _, err := DecodeShot(json.RawMessage(`{"image_b64":` + `"` + base64.StdEncoding.EncodeToString(make([]byte, MaxShotBytes)) + `"}`)); err != nil {
		t.Fatalf("shot at the cap refused: %v", err)
	}
}

func TestFocusClaimValidity(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	good := FocusClaim{AppID: "a", Mac: "m", User: "u", ClaimedAt: now.Add(-time.Minute), LeaseUntil: now.Add(FocusLease - time.Minute)}
	if !good.Valid(now) {
		t.Fatal("fresh claim invalid")
	}
	for name, claim := range map[string]FocusClaim{
		"no app":        {ClaimedAt: good.ClaimedAt, LeaseUntil: good.LeaseUntil},
		"blank app":     {AppID: " ", ClaimedAt: good.ClaimedAt, LeaseUntil: good.LeaseUntil},
		"expired":       {AppID: "a", ClaimedAt: now.Add(-FocusLease - time.Second), LeaseUntil: now.Add(-time.Second)},
		"lease at now":  {AppID: "a", ClaimedAt: now.Add(-time.Minute), LeaseUntil: now},
		"no claim time": {AppID: "a", LeaseUntil: good.LeaseUntil},
		"lease first":   {AppID: "a", ClaimedAt: now.Add(time.Hour), LeaseUntil: now.Add(time.Minute)},
	} {
		if claim.Valid(now) {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Last focused wins: the newest unexpired claim, the later one on a tie.
func TestActiveClaim(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	claim := func(app string, ago, left time.Duration) FocusClaim {
		return FocusClaim{AppID: app, ClaimedAt: now.Add(-ago), LeaseUntil: now.Add(left)}
	}
	if _, ok := ActiveClaim(nil, now); ok {
		t.Fatal("no claims produced a holder")
	}
	for _, tc := range []struct {
		name   string
		claims []FocusClaim
		want   string
		ok     bool
	}{
		{"newest wins", []FocusClaim{claim("laptop", 3*time.Minute, 7*time.Minute), claim("studio", time.Minute, 9*time.Minute)}, "studio", true},
		{"order does not matter", []FocusClaim{claim("studio", time.Minute, 9*time.Minute), claim("laptop", 3*time.Minute, 7*time.Minute)}, "studio", true},
		{"expired newest skipped", []FocusClaim{claim("laptop", 3*time.Minute, 7*time.Minute), claim("studio", time.Minute, -time.Second)}, "laptop", true},
		{"tie goes to later", []FocusClaim{claim("laptop", time.Minute, 9*time.Minute), claim("studio", time.Minute, 9*time.Minute)}, "studio", true},
		{"all expired", []FocusClaim{claim("laptop", 20*time.Minute, -10*time.Minute)}, "", false},
	} {
		got, ok := ActiveClaim(tc.claims, now)
		if ok != tc.ok || got.AppID != tc.want {
			t.Errorf("%s: %q %v", tc.name, got.AppID, ok)
		}
	}
}

// browser_profile is a plain name, never a path or a control sequence.
func TestBrowserProfileNameRule(t *testing.T) {
	for _, ok := range []string{"work", "Research-2", "a.b_c", strings.Repeat("a", MaxBrowserProfileLen)} {
		if err := (BrowserArgs{URL: "https://example.com/", BrowserProfile: ok}).Validate(OpOpen); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"a\nb", "a\tb", "a\x1bb", "a\x7fb", "../../etc", ".hidden", "a b", "a/b", strings.Repeat("a", MaxBrowserProfileLen+1)} {
		if err := (BrowserArgs{URL: "https://example.com/", BrowserProfile: bad}).Validate(OpOpen); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// MaxShotBytes keeps a shot reply under the 2 MiB stdin cap of the remote
// reply publish, envelope included.
func TestShotAtTheCapFitsTheRemoteReply(t *testing.T) {
	const remotePublishCap = 2 << 20 // cmd/agent-deck remotePublishDataCap
	result, _ := json.Marshal(ShotResult{MIME: "image/jpeg", ImageB64: base64.StdEncoding.EncodeToString(make([]byte, MaxShotBytes)), Width: 4096, Height: 4096})
	reply, _ := json.Marshal(BrowserResponse{V: ProtocolVersion, ReqID: strings.Repeat("a", 32), RequestEventID: strings.Repeat("e", 64), OK: true, Result: result, Driver: DriverAgent, AppID: strings.Repeat("m", 128)})
	if len(reply) > remotePublishCap {
		t.Fatalf("a shot reply at the cap is %d bytes, over the %d remote publish cap", len(reply), remotePublishCap)
	}
}
