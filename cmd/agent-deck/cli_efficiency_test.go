package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestSelectJSONFields(t *testing.T) {
	rows := []struct {
		ID, Title, Path string
	}{{"abc", "worker", "/a/very/long/path"}}
	got, err := selectJSONFields(rows, "ID,Title")
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]interface{}{{"ID": "abc", "Title": "worker"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestSelectJSONFieldsRejectsUnknown(t *testing.T) {
	_, err := selectJSONFields([]struct{ ID string }{{"abc"}}, "missing")
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("got error %v", err)
	}
}

func TestTailOutputLines(t *testing.T) {
	got, omitted := tailOutputLines("one\ntwo\nthree\nfour", 2)
	if got != "three\nfour" || omitted != 2 {
		t.Fatalf("got %q, %d", got, omitted)
	}
	full, omitted := tailOutputLines("one\ntwo", 0)
	if full != "one\ntwo" || omitted != 0 {
		t.Fatalf("--full semantics got %q, %d", full, omitted)
	}
}
