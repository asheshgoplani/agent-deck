package main

import (
	"context"
	"errors"
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
