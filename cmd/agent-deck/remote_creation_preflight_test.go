package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type creationCatalogRunner struct {
	calls int
	err   error
}

func (r *creationCatalogRunner) FetchCreationCatalog(context.Context) (*session.RemoteCreationCatalog, error) {
	r.calls++
	return &session.RemoteCreationCatalog{Version: 1, Commands: map[string][]session.RemoteCreationField{"add": {{Name: "json"}}, "launch": {{Name: "message-file", TakesValue: true}}}}, r.err
}
func TestRemoteCreationPublicPreflight(t *testing.T) {
	for _, args := range [][]string{{"add", "--json"}, {"launch", "--message-file", "-"}} {
		r := &creationCatalogRunner{}
		if err := preflightRemoteCreation(context.Background(), r, args); err != nil || r.calls != 1 {
			t.Fatalf("%v: calls=%d err=%v", args, r.calls, err)
		}
	}
	for _, args := range [][]string{{"add", "--typo"}, {"launch", "--message-file", "/controller/task"}} {
		r := &creationCatalogRunner{}
		if err := preflightRemoteCreation(context.Background(), r, args); err == nil {
			t.Fatalf("unsafe args allowed: %v", args)
		}
	}
	r := &creationCatalogRunner{err: errors.New("old binary")}
	if err := preflightRemoteCreation(context.Background(), r, []string{"add", "--json"}); err == nil {
		t.Fatal("old binary allowed")
	}
	for _, args := range [][]string{{"add", "--help"}, {"launch", "-h"}, {"add", "--capabilities", "--json"}, {"session", "show", "x"}} {
		r := &creationCatalogRunner{err: errors.New("must not fetch")}
		if err := preflightRemoteCreation(context.Background(), r, args); err != nil || r.calls != 0 {
			t.Fatalf("%v: calls=%d err=%v", args, r.calls, err)
		}
	}
}

func TestRemoteCreationMessageFileAfterEveryBoolean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "query.txt")
	if err := os.WriteFile(path, []byte("literal query"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, field := range creationCommandFields("launch") {
		if field.TakesValue {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			args, input, closeInput, err := remoteMessageInput([]string{"launch", "--" + field.Name, "--message-file", path})
			if err != nil {
				t.Fatal(err)
			}
			defer closeInput()
			data, err := io.ReadAll(input)
			if err != nil || string(data) != "literal query" {
				t.Fatalf("message lost: %s %v", data, err)
			}
			if strings.Contains(strings.Join(args, " "), path) {
				t.Fatalf("controller filename leaked: %v", args)
			}
		})
	}
}
