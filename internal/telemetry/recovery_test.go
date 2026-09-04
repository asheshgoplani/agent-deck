package telemetry

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRecoveryStaleWriterCannotReviveDecline(t *testing.T) {
	interactiveEnv(t)
	granted(t)
	stale := LoadState()
	declined := LoadState()
	Decline(declined, "9.9.9", time.Now())
	if err := SaveState(declined); err != nil {
		t.Fatal(err)
	}
	stale.Counters = map[string]int{CounterTUILaunches: 1}
	_ = SaveState(stale)
	if LoadState().Consent != ConsentDeclined {
		t.Fatal("stale counter writer revived declined consent")
	}
}

func TestRecoveryEndpointChangeRequiresConsent(t *testing.T) {
	interactiveEnv(t)
	first := newReceiver(t)
	endpoint = first.srv.URL
	granted(t)
	second := newReceiver(t)
	endpoint = second.srv.URL
	result := MaybeSend(context.Background(), "9.9.9")
	if result.Attempted || second.hits.Load() != 0 {
		t.Fatal("sent to endpoint never disclosed at consent")
	}
}

func TestRecoverySchemaMismatchNeverSends(t *testing.T) {
	interactiveEnv(t)
	r := newReceiver(t)
	endpoint = r.srv.URL
	granted(t)
	s := LoadState()
	s.SchemaVersion = SchemaVersion + 1
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	if result := MaybeSend(context.Background(), "9.9.9"); result.Attempted {
		t.Fatal("sent under a different consent schema")
	}
}

// The subprocesses share only a disposable data directory and loopback receiver.
// Their process-local Go globals are independent, as with two real TUIs.
func TestRecoveryProcessHelper(t *testing.T) {
	action := os.Getenv("TELEMETRY_TEST_ACTION")
	if action == "" {
		return
	}
	home := os.Getenv("TELEMETRY_TEST_HOME")
	os.Setenv("HOME", home)
	os.Setenv("XDG_DATA_HOME", home+"/data")
	os.Setenv("XDG_CONFIG_HOME", home+"/config")
	os.Setenv("XDG_CACHE_HOME", home+"/cache")
	isTerminalFn = func() bool { return true }
	endpoint = os.Getenv("TELEMETRY_TEST_ENDPOINT")
	switch action {
	case "send":
		MaybeSend(context.Background(), "9.9.9")
	case "record":
		for i := 0; i < 25; i++ {
			Record(CounterTUILaunches)
		}
	case "stale":
		s := LoadState()
		fmt.Println("READY")
		bufio.NewReader(os.Stdin).ReadString('\n')
		s.Counters = map[string]int{CounterTUILaunches: 1}
		if SaveState(s) == nil {
			t.Fatal("stale save unexpectedly accepted")
		}
	case "disable":
		if err := Disable("9.9.9", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

func recoveryProcess(t *testing.T, action string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestRecoveryProcessHelper$")
	cmd.Env = append(os.Environ(), "TELEMETRY_TEST_ACTION="+action, "TELEMETRY_TEST_HOME="+os.Getenv("HOME"), "TELEMETRY_TEST_ENDPOINT="+endpoint)
	return cmd
}

func TestRecoveryCrossProcessBudgetAndCounters(t *testing.T) {
	interactiveEnv(t)
	r := newReceiver(t)
	granted(t)
	for _, action := range []string{"record", "send"} {
		cmds := make([]*exec.Cmd, 12)
		outputs := make([]bytes.Buffer, len(cmds))
		for i := range cmds {
			cmds[i] = recoveryProcess(t, action)
			cmds[i].Stdout = &outputs[i]
			cmds[i].Stderr = &outputs[i]
			if err := cmds[i].Start(); err != nil {
				t.Fatal(err)
			}
		}
		for i, cmd := range cmds {
			if err := cmd.Wait(); err != nil {
				t.Fatalf("%s: %v %s", action, err, outputs[i].String())
			}
		}
		if action == "record" && LoadState().Counters[CounterTUILaunches] != 300 {
			t.Fatal("cross-process increments lost")
		}
	}
	if r.hits.Load() != 1 {
		t.Fatalf("daily requests: %d, want 1", r.hits.Load())
	}
	var payload Payload
	if err := json.Unmarshal(LoadState().LastPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Counters[CounterTUILaunches] != 300 {
		t.Fatalf("payload counters=%v", payload.Counters)
	}
}

func TestRecoveryCrossProcessStaleWriterAfterDisable(t *testing.T) {
	interactiveEnv(t)
	granted(t)
	stale := recoveryProcess(t, "stale")
	stdin, err := stale.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := stale.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	stale.Stderr = &stderr
	if err := stale.Start(); err != nil {
		t.Fatal(err)
	}
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "READY\n" {
		t.Fatalf("ready=%q err=%v", ready, err)
	}
	if out, err := recoveryProcess(t, "disable").CombinedOutput(); err != nil {
		t.Fatalf("disable: %v %s", err, out)
	}
	fmt.Fprintln(stdin, "continue")
	stdin.Close()
	io.Copy(io.Discard, stdout)
	if err := stale.Wait(); err != nil {
		t.Fatalf("stale process: %v %s", err, stderr.String())
	}
	if s := LoadState(); s.Consent != ConsentDeclined || s.InstallID != "" {
		t.Fatalf("revived consent: %+v", s)
	}
}

func TestRecoveryPayloadBoundsAndIdentifiers(t *testing.T) {
	interactiveEnv(t)
	s := granted(t)
	s.Counters = map[string]int{}
	for _, k := range AllowedCounterKeys() {
		s.Counters[k] = int(^uint(0) >> 1)
	}
	p := BuildPayload(s, "/Users/example/private-host", time.Now())
	body, err := p.Marshal()
	if err != nil || len(body) > 2048 || p.Version != "dev" {
		t.Fatalf("unbounded payload: %s %v", body, err)
	}
	for _, count := range p.Counters {
		if count != maxCounter {
			t.Fatal("counter exceeds cap")
		}
	}
	r := newReceiver(t)
	s.ConsentEndpoint = endpoint
	s.InstallID = "person@example.com"
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	if result := MaybeSend(context.Background(), "9.9.9"); result.Attempted || r.hits.Load() != 0 {
		t.Fatal("invalid install identifier sent")
	}
}

func TestRecoveryDisableDuringInFlightSend(t *testing.T) {
	interactiveEnv(t)
	received := make(chan struct{})
	finish := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(received); <-finish; w.WriteHeader(204) }))
	defer server.Close()
	endpoint = server.URL
	granted(t)
	sendDone := make(chan SendResult, 1)
	go func() { sendDone <- MaybeSend(context.Background(), "9.9.9") }()
	<-received
	disable := recoveryProcess(t, "disable")
	var output bytes.Buffer
	disable.Stdout, disable.Stderr = &output, &output
	if err := disable.Start(); err != nil {
		t.Fatal(err)
	}
	close(finish)
	if result := <-sendDone; !result.Sent {
		t.Fatalf("send=%+v", result)
	}
	if err := disable.Wait(); err != nil {
		t.Fatalf("disable=%v %s", err, output.String())
	}
	if s := LoadState(); s.Consent != ConsentDeclined || s.InstallID != "" {
		t.Fatalf("disable lost: %+v", s)
	}
	if result := MaybeSend(context.Background(), "9.9.9"); result.Attempted {
		t.Fatal("sent after completed disable")
	}
}
