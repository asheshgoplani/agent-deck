package watcher

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

const testGitHubSecret = "test-webhook-secret-42"

// signPayload computes the HMAC-SHA256 signature for a GitHub webhook payload.
func signPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// waitForGitHubServer polls the adapter address until it accepts connections or timeout.
func waitForGitHubServer(t *testing.T, a *GitHubAdapter, timeout time.Duration) bool {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			return false
		case <-time.After(10 * time.Millisecond):
			if err := a.HealthCheck(); err == nil {
				return true
			}
		}
	}
}

func TestGitHub_Setup_DefaultPort(t *testing.T) {
	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{"secret": testGitHubSecret},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if a.addr != "127.0.0.1:18461" {
		t.Errorf("expected default addr 127.0.0.1:18461, got %q", a.addr)
	}
}

func TestGitHub_Setup_CustomPort(t *testing.T) {
	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{"secret": testGitHubSecret, "port": "19998"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if a.addr != "127.0.0.1:19998" {
		t.Errorf("expected addr 127.0.0.1:19998, got %q", a.addr)
	}
}

func TestGitHub_Setup_MissingSecret(t *testing.T) {
	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{},
	})
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Errorf("error should mention 'secret', got: %v", err)
	}
}

func TestGitHub_VerifySignature_Valid(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	sig := signPayload(testGitHubSecret, body)
	if !verifyGitHubSignature(testGitHubSecret, sig, body) {
		t.Error("expected valid signature to pass verification")
	}
}

func TestGitHub_VerifySignature_Invalid(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	wrongSig := signPayload("wrong-secret", body)
	if verifyGitHubSignature(testGitHubSecret, wrongSig, body) {
		t.Error("expected invalid signature to fail verification")
	}
}

func TestGitHub_VerifySignature_MalformedPrefix(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	mac := hmac.New(sha256.New, []byte(testGitHubSecret))
	mac.Write(body)
	hexSig := hex.EncodeToString(mac.Sum(nil))

	// Missing "sha256=" prefix
	if verifyGitHubSignature(testGitHubSecret, hexSig, body) {
		t.Error("expected signature without sha256= prefix to fail")
	}

	// Wrong prefix
	if verifyGitHubSignature(testGitHubSecret, "sha1="+hexSig, body) {
		t.Error("expected signature with sha1= prefix to fail")
	}
}

func TestGitHub_VerifySignature_InvalidHex(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	if verifyGitHubSignature(testGitHubSecret, "sha256=zzzznothex", body) {
		t.Error("expected invalid hex to fail verification")
	}
}

func TestGitHub_Listen_ValidSignature_202(t *testing.T) {
	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{"secret": testGitHubSecret, "port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForGitHubServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	body := []byte(`{"action":"opened","issue":{"number":42,"title":"Bug title","body":"issue body"},"sender":{"login":"octocat"},"repository":{"full_name":"owner/repo"}}`)
	sig := signPayload(testGitHubSecret, body)

	req, _ := http.NewRequest("POST", "http://"+a.Addr()+"/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "issues")
	req.Header.Set("X-GitHub-Delivery", "test-delivery-id")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202, got %d", resp.StatusCode)
	}

	select {
	case evt := <-events:
		if evt.Source != "github" {
			t.Errorf("expected Source=github, got %q", evt.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event received within 2s")
	}

	cancel()
	<-listenErr
}

func TestGitHub_Listen_InvalidSignature_401(t *testing.T) {
	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{"secret": testGitHubSecret, "port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForGitHubServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	body := []byte(`{"action":"opened"}`)
	wrongSig := signPayload("wrong-secret", body)

	req, _ := http.NewRequest("POST", "http://"+a.Addr()+"/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", wrongSig)
	req.Header.Set("X-GitHub-Event", "issues")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}

	// Verify no event was produced
	select {
	case evt := <-events:
		t.Errorf("unexpected event from invalid signature: %+v", evt)
	case <-time.After(200 * time.Millisecond):
		// Expected: no event
	}

	cancel()
	<-listenErr
}

func TestGitHub_Listen_MissingSignature_401(t *testing.T) {
	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{"secret": testGitHubSecret, "port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForGitHubServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	body := []byte(`{"action":"opened"}`)
	req, _ := http.NewRequest("POST", "http://"+a.Addr()+"/github", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issues")
	// No X-Hub-Signature-256 header

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}

	// Verify no event
	select {
	case evt := <-events:
		t.Errorf("unexpected event from missing signature: %+v", evt)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	<-listenErr
}

func TestGitHub_Listen_BodySizeLimit(t *testing.T) {
	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{"secret": testGitHubSecret, "port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForGitHubServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	// Send body > 10MB
	bigBody := bytes.Repeat([]byte("x"), 10<<20+1)
	sig := signPayload(testGitHubSecret, bigBody)

	req, _ := http.NewRequest("POST", "http://"+a.Addr()+"/github", bytes.NewReader(bigBody))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "push")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", resp.StatusCode)
	}

	cancel()
	<-listenErr
}

func TestGitHub_Normalize_Issues(t *testing.T) {
	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{"secret": testGitHubSecret, "port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForGitHubServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	payload := map[string]interface{}{
		"action": "opened",
		"issue": map[string]interface{}{
			"number": 42,
			"title":  "Bug title",
			"body":   "issue body text",
		},
		"sender":     map[string]interface{}{"login": "octocat"},
		"repository": map[string]interface{}{"full_name": "owner/repo"},
	}
	body, _ := json.Marshal(payload)
	sig := signPayload(testGitHubSecret, body)

	req, _ := http.NewRequest("POST", "http://"+a.Addr()+"/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "issues")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	select {
	case evt := <-events:
		if evt.Subject != "[opened] #42: Bug title" {
			t.Errorf("unexpected Subject: %q", evt.Subject)
		}
		if evt.Body != "issue body text" {
			t.Errorf("unexpected Body: %q", evt.Body)
		}
		if evt.Sender != "octocat@github.com" {
			t.Errorf("unexpected Sender: %q", evt.Sender)
		}
		if evt.Source != "github" {
			t.Errorf("unexpected Source: %q", evt.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event received")
	}

	cancel()
	<-listenErr
}

func TestGitHub_Normalize_PullRequest(t *testing.T) {
	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{"secret": testGitHubSecret, "port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForGitHubServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	payload := map[string]interface{}{
		"action": "opened",
		"number": 17,
		"pull_request": map[string]interface{}{
			"title": "Feature title",
			"body":  "PR description",
		},
		"sender":     map[string]interface{}{"login": "octocat"},
		"repository": map[string]interface{}{"full_name": "owner/repo"},
	}
	body, _ := json.Marshal(payload)
	sig := signPayload(testGitHubSecret, body)

	req, _ := http.NewRequest("POST", "http://"+a.Addr()+"/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "pull_request")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	select {
	case evt := <-events:
		if evt.Subject != "[PR opened] #17: Feature title" {
			t.Errorf("unexpected Subject: %q", evt.Subject)
		}
		if evt.Body != "PR description" {
			t.Errorf("unexpected Body: %q", evt.Body)
		}
		if evt.Sender != "octocat@github.com" {
			t.Errorf("unexpected Sender: %q", evt.Sender)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event received")
	}

	cancel()
	<-listenErr
}

func TestGitHub_Normalize_Push(t *testing.T) {
	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{"secret": testGitHubSecret, "port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForGitHubServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	payload := map[string]interface{}{
		"ref": "refs/heads/main",
		"commits": []map[string]interface{}{
			{"message": "fix: resolve bug #123"},
		},
		"sender":     map[string]interface{}{"login": "octocat"},
		"pusher":     map[string]interface{}{"email": "octocat@users.noreply.github.com"},
		"repository": map[string]interface{}{"full_name": "owner/repo"},
	}
	body, _ := json.Marshal(payload)
	sig := signPayload(testGitHubSecret, body)

	req, _ := http.NewRequest("POST", "http://"+a.Addr()+"/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "push")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	select {
	case evt := <-events:
		if evt.Subject != "[push] main: 1 commit(s)" {
			t.Errorf("unexpected Subject: %q", evt.Subject)
		}
		if evt.Body != "fix: resolve bug #123" {
			t.Errorf("unexpected Body: %q", evt.Body)
		}
		// Pusher email contains "@", so it should be used
		if evt.Sender != "octocat@users.noreply.github.com" {
			t.Errorf("unexpected Sender: %q", evt.Sender)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event received")
	}

	cancel()
	<-listenErr
}

func TestGitHub_Normalize_Unknown(t *testing.T) {
	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{"secret": testGitHubSecret, "port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForGitHubServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	payload := map[string]interface{}{
		"action":     "completed",
		"sender":     map[string]interface{}{"login": "octocat"},
		"repository": map[string]interface{}{"full_name": "owner/repo"},
	}
	body, _ := json.Marshal(payload)
	sig := signPayload(testGitHubSecret, body)

	req, _ := http.NewRequest("POST", "http://"+a.Addr()+"/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "check_run")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	select {
	case evt := <-events:
		if evt.Subject != "[check_run] event from owner/repo" {
			t.Errorf("unexpected Subject: %q", evt.Subject)
		}
		if evt.Sender != "octocat@github.com" {
			t.Errorf("unexpected Sender: %q", evt.Sender)
		}
		// Body should be the raw payload truncated to 1000 chars
		if len(evt.Body) == 0 {
			t.Error("expected non-empty Body for unknown event")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event received")
	}

	cancel()
	<-listenErr
}

// TestGitHub_Normalize_MalformedPayload_DropsEvent asserts that a malformed
// JSON body (e.g., a partial write or truncated upload) does NOT result in
// a zero-valued Event being emitted to the channel. Before the fix, the
// four normalizers all did `_ = json.Unmarshal(body, &p)` and then
// constructed an Event with zero `Sender`/`Subject`/`Ref`, making the
// downstream router silently misroute or dedup-drop the event.
//
// Source: critical-hunt audit #1 (P0).
func TestGitHub_Normalize_MalformedPayload_DropsEvent(t *testing.T) {
	cases := []struct {
		name      string
		eventType string
	}{
		{"issues", "issues"},
		{"pull_request", "pull_request"},
		{"push", "push"},
		{"unknown", "check_run"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := &GitHubAdapter{}
			err := a.Setup(context.Background(), AdapterConfig{
				Type:     "github",
				Name:     "test",
				Settings: map[string]string{"secret": testGitHubSecret, "port": "0"},
			})
			if err != nil {
				t.Fatalf("Setup: %v", err)
			}

			events := make(chan Event, 10)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			listenErr := make(chan error, 1)
			go func() {
				listenErr <- a.Listen(ctx, events)
			}()

			if !waitForGitHubServer(t, a, 2*time.Second) {
				t.Fatal("server did not start in time")
			}

			// Truncated/garbage JSON — what a partial write or chunked-upload
			// abort looks like in practice.
			body := []byte(`{"action":"opened","issue":{"number":42,"title":"Bug`)
			sig := signPayload(testGitHubSecret, body)

			req, _ := http.NewRequest("POST", "http://"+a.Addr()+"/github", bytes.NewReader(body))
			req.Header.Set("X-Hub-Signature-256", sig)
			req.Header.Set("X-GitHub-Event", tc.eventType)

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			resp.Body.Close()

			// 202 Accepted is expected even on parse failure (server already
			// committed to the protocol contract before parsing).
			if resp.StatusCode != http.StatusAccepted {
				t.Errorf("status: want 202, got %d", resp.StatusCode)
			}

			select {
			case evt := <-events:
				t.Fatalf("malformed payload must not emit event; got: subject=%q sender=%q body=%q",
					evt.Subject, evt.Sender, evt.Body)
			case <-time.After(300 * time.Millisecond):
				// Expected: event was dropped, not emitted with zero fields.
			}

			cancel()
			<-listenErr
		})
	}
}

func TestGitHub_HealthCheck_Running(t *testing.T) {
	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{"secret": testGitHubSecret, "port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForGitHubServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	if err := a.HealthCheck(); err != nil {
		t.Errorf("expected nil from HealthCheck when running, got %v", err)
	}

	cancel()
	<-listenErr
}

func TestGitHub_Listen_StopNoLeaks(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
		goleak.IgnoreTopFunction("database/sql.(*DB).connectionResetter"),
		goleak.IgnoreAnyFunction("modernc.org"),
		goleak.IgnoreAnyFunction("poll.runtime_pollWait"),
		// Plan 17-01: adding the Google client pulls in go.opencensus.io, whose
		// stats worker is started from an init() and lives for the test binary.
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
	)

	a := &GitHubAdapter{}
	err := a.Setup(context.Background(), AdapterConfig{
		Type:     "github",
		Name:     "test",
		Settings: map[string]string{"secret": testGitHubSecret, "port": "0"},
	})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}

	events := make(chan Event, 10)
	ctx, cancel := context.WithCancel(context.Background())

	listenErr := make(chan error, 1)
	go func() {
		listenErr <- a.Listen(ctx, events)
	}()

	if !waitForGitHubServer(t, a, 2*time.Second) {
		t.Fatal("server did not start in time")
	}

	// Send one valid POST to exercise the handler goroutine path
	body := []byte(`{"action":"test","sender":{"login":"bot"},"repository":{"full_name":"x/y"}}`)
	sig := signPayload(testGitHubSecret, body)
	req, _ := http.NewRequest("POST", "http://"+a.Addr()+"/github", bytes.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sig)
	req.Header.Set("X-GitHub-Event", "ping")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	// Drain event
	select {
	case <-events:
	case <-time.After(time.Second):
	}

	cancel()
	<-listenErr
	// goleak.VerifyNone runs via defer
}
