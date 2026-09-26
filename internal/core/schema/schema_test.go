package schema

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

type leaf struct {
	N int `json:"n"`
}

type tree struct {
	Name   string            `json:"name" doc:"The name"`
	Opt    string            `json:"opt,omitempty"`
	When   time.Time         `json:"when"`
	Ptr    *leaf             `json:"ptr"`
	Kids   []tree            `json:"kids,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	Any    any               `json:"any,omitempty"`
	Hidden string            `json:"-"`
	Ratio  float64           `json:"ratio"`
	Raw    []byte            `json:"raw,omitempty"`
	NoTag  bool
}

func TestReflectObjectRules(t *testing.T) {
	s, err := Reflect(reflect.TypeOf(tree{}), "id", "title")
	if err != nil {
		t.Fatal(err)
	}
	if s.Draft != Draft || s.ID != "id" || s.Title != "title" || s.Type != "object" {
		t.Fatalf("root header = %+v", s)
	}
	if _, ok := s.Properties["Hidden"]; ok {
		t.Fatal(`json:"-" field emitted`)
	}
	if _, ok := s.Properties["-"]; ok {
		t.Fatal(`json:"-" field emitted as "-"`)
	}
	if _, ok := s.Properties["untagged"]; ok {
		t.Fatal("unexported field emitted")
	}
	if p := s.Properties["NoTag"]; p == nil || p.Type != "boolean" {
		t.Fatalf("untagged exported field = %+v", p)
	}
	if got := s.Properties["name"]; got.Type != "string" || got.Description != "The name" {
		t.Fatalf("name = %+v", got)
	}
	if got := s.Properties["when"]; got.Type != "string" || got.Format != "date-time" {
		t.Fatalf("when = %+v", got)
	}
	if got := s.Properties["ptr"]; got.Ref != "#/$defs/schema.leaf" {
		t.Fatalf("ptr = %+v", got)
	}
	if got := s.Properties["kids"]; got.Type != "array" || got.Items.Ref != "#/$defs/schema.tree" {
		t.Fatalf("kids (recursive) = %+v", got)
	}
	if got := s.Properties["labels"]; got.Type != "object" || got.AdditionalProperties.(*Schema).Type != "string" {
		t.Fatalf("labels = %+v", got)
	}
	if got := s.Properties["ratio"]; got.Type != "number" {
		t.Fatalf("ratio = %+v", got)
	}
	if got := s.Properties["raw"]; got.Type != "string" || got.Format != "byte" {
		t.Fatalf("raw = %+v", got)
	}
	wantRequired := []string{"NoTag", "name", "ratio", "when"}
	if !reflect.DeepEqual(s.Required, wantRequired) {
		t.Fatalf("required = %v, want %v", s.Required, wantRequired)
	}
	if s.Defs["schema.tree"] == nil || s.Defs["schema.leaf"] == nil {
		t.Fatalf("defs = %v", s.Defs)
	}
}

func TestReflectIsDeterministic(t *testing.T) {
	var first []byte
	for i := 0; i < 5; i++ {
		s, err := Reflect(reflect.TypeOf(&tree{}), "id", "t")
		if err != nil {
			t.Fatal(err)
		}
		b, err := Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = b
			continue
		}
		if string(b) != string(first) {
			t.Fatal("schema output is not deterministic")
		}
	}
	if !strings.HasSuffix(string(first), "}\n") || !json.Valid(first) {
		t.Fatal("Marshal must produce valid JSON with a trailing newline")
	}
}

func TestReflectRejectsUnsupportedTypes(t *testing.T) {
	type bad struct {
		C chan int `json:"c"`
	}
	if _, err := Reflect(reflect.TypeOf(bad{}), "", ""); err == nil {
		t.Fatal("chan field accepted")
	}
	type badMap struct {
		M map[int]string `json:"m"`
	}
	if _, err := Reflect(reflect.TypeOf(badMap{}), "", ""); err == nil {
		t.Fatal("non-string map key accepted")
	}
}
