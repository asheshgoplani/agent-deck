package session

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func creationTestCatalog() *RemoteCreationCatalog {
	fields := []RemoteCreationField{}
	for _, name := range []string{"json", "quick", "sandbox", "yolo", "create-dir", "no-wait", "no-parent"} {
		fields = append(fields, RemoteCreationField{Name: name})
	}
	for _, name := range []string{"t", "g", "c", "account", "model", "mcp", "resume-session", "extra-arg", "w", "startup-query", "additional-path", "effort", "parent"} {
		fields = append(fields, RemoteCreationField{Name: name, TakesValue: true})
	}
	return &RemoteCreationCatalog{Version: 1, Commands: map[string][]RemoteCreationField{"add": fields, "launch": fields}}
}

func TestRemoteCreationCatalogStrictValidation(t *testing.T) {
	c := creationTestCatalog()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"add", "--effort"}, "needs a value"},
		{[]string{"add", "--typo", "x"}, "unsupported"},
		{[]string{"add", "--json=maybe"}, "boolean"},
		{[]string{"add", "--account", "/home/controller/.claude"}, "config directory"},
		{[]string{"launch", "--message-file", "/home/controller/task"}, "unsupported"},
		{[]string{"add", "one", "two"}, "one project path"},
	} {
		if err := c.ValidateArgs(tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %s", tc.args, err, tc.want)
		}
	}
	if err := c.ValidateArgs([]string{"add", "--extra-arg", "--chrome", "--effort=high", "--", "-remote-path"}); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteCreationUnsupportedNeverCreates(t *testing.T) {
	for _, mode := range []string{"old", "missing-field", "bad-version"} {
		t.Run(mode, func(t *testing.T) {
			var calls [][]string
			runner := &SSHRunner{runFn: func(_ context.Context, args ...string) ([]byte, error) {
				calls = append(calls, append([]string(nil), args...))
				if !reflect.DeepEqual(args, []string{"add", "--capabilities", "--json"}) {
					t.Fatalf("mutation reached runner: %v", args)
				}
				if mode == "old" {
					return nil, errors.New("flag provided but not defined: -capabilities")
				}
				c := creationTestCatalog()
				if mode == "bad-version" {
					c.Version = 999
				} else {
					c.Commands["add"] = nil
				}
				return json.Marshal(c)
			}}
			_, err := runner.CreateSessionWithOptions(context.Background(), RemoteAddOptions{Tool: "codex", ReasoningEffort: "high"})
			if err == nil || len(calls) != 1 {
				t.Fatalf("error=%v calls=%v", err, calls)
			}
		})
	}
}

func TestRemoteCreationNewOptionsRoundTrip(t *testing.T) {
	var calls [][]string
	runner := &SSHRunner{runFn: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, append([]string(nil), args...))
		if reflect.DeepEqual(args, []string{"add", "--capabilities", "--json"}) {
			return json.Marshal(creationTestCatalog())
		}
		if args[0] == "launch" {
			return []byte(`{"id":"new-id","title":"remote","status":"running"}`), nil
		}
		t.Fatalf("unexpected call %v", args)
		return nil, nil
	}}
	id, err := runner.CreateSessionWithOptions(context.Background(), RemoteAddOptions{Tool: "claude", Title: "remote", StartQuery: "literal\nquery ' $", AdditionalPaths: []string{"~/repo two", "/srv/third"}, ParentID: "remote-parent"})
	if err != nil || id != "new-id" {
		t.Fatalf("id=%q error=%v", id, err)
	}
	if len(calls) != 2 {
		t.Fatalf("query must be launched once, calls=%v", calls)
	}
	for _, pair := range [][]string{{"--startup-query", "literal\nquery ' $"}, {"--additional-path", "~/repo two"}, {"--additional-path", "/srv/third"}, {"--parent", "remote-parent"}} {
		found := false
		for i := 0; i+1 < len(calls[1]); i++ {
			if reflect.DeepEqual(calls[1][i:i+2], pair) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %v in %v", pair, calls[1])
		}
	}
}

func TestRemoteCreationExplicitFalseAndLineBreakTransport(t *testing.T) {
	off := false
	args, err := remoteAddArgs(RemoteAddOptions{Tool: "codex", YoloOverride: &off, ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "--yolo=false") {
		t.Fatalf("false lost: %v", args)
	}
	args, err = remoteAddArgs(RemoteAddOptions{Tool: "claude", ClaudeOptions: &ClaudeOptions{}})
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"--skip-permissions=false", "--auto-mode=false", "--chrome=false", "--teammate-mode=false"} {
		if !strings.Contains(strings.Join(args, " "), flag) {
			t.Errorf("missing %s: %v", flag, args)
		}
	}
	if remoteChannelArgsSafe([]string{"launch", "--startup-query", "one\ntwo"}) {
		t.Fatal("multiline query must bypass line-limited channel before sending")
	}
	if !remoteChannelArgsSafe([]string{"launch", "--startup-query", "one two"}) {
		t.Fatal("single line query should use channel")
	}
}

func TestRemoteCreationQueryResumeRefusedBeforeFetch(t *testing.T) {
	for _, opts := range []RemoteAddOptions{
		{Tool: "claude", StartQuery: "query", ClaudeOptions: &ClaudeOptions{SessionMode: "resume"}},
		{Tool: "claude", StartQuery: "query", ExtraArgs: []string{"--continue"}},
		{Tool: "claude", StartQuery: "query", ResumeSessionID: "old-id"},
	} {
		runner := &SSHRunner{runFn: func(context.Context, ...string) ([]byte, error) {
			t.Fatal("invalid combination contacted remote")
			return nil, nil
		}}
		if _, err := runner.CreateSessionWithOptions(context.Background(), opts); err == nil {
			t.Fatal("query resume accepted")
		}
	}
}

func TestRemoteCreationFlagValueSkipsLiteralOptions(t *testing.T) {
	c := creationTestCatalog()
	c.Commands["add"] = append(c.Commands["add"], RemoteCreationField{Name: "attach"})
	for _, args := range [][]string{{"add", "--extra-arg", "--attach"}, {"add", "--", "--attach"}} {
		if _, found := c.FlagValue(args, "attach"); found {
			t.Fatalf("literal option treated as attach: %v", args)
		}
	}
	value, found := c.FlagValue([]string{"add", "--attach", "--attach=false"}, "attach")
	if !found || value != "false" {
		t.Fatalf("last flag wins: %q %t", value, found)
	}
}
