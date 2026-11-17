package openpages

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestRecordListNewestFirstOnePerPath(t *testing.T) {
	profile := fmt.Sprintf("p%d", time.Now().UnixNano())
	base := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	for i, path := range []string{"/w/a.html", "/w/b.md", "/w/a.html"} {
		if _, err := Record(profile, Page{SessionID: "sess-1", Path: path, Kind: KindOf(path), OpenedAt: base.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Record(profile, Page{SessionID: "sess-2", URL: "https://example.com", Kind: KindURL}); err != nil {
		t.Fatal(err)
	}
	pages, err := List(profile, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].Path != "/w/a.html" || pages[1].Path != "/w/b.md" {
		t.Fatalf("want a.html (reopened) then b.md, got %+v", pages)
	}
	if pages[1].Kind != KindMarkdown || pages[0].Name != "a.html" || !ValidPageID(pages[0].ID) {
		t.Fatalf("fields: %+v", pages)
	}
	last, err := Last(profile, "sess-1")
	if err != nil || last.ID != pages[0].ID {
		t.Fatalf("last: %+v %v", last, err)
	}
	if got, err := Get(profile, "sess-1", pages[1].ID); err != nil || got.Path != "/w/b.md" {
		t.Fatalf("get: %+v %v", got, err)
	}
	if _, err := Get(profile, "sess-2", pages[1].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("page of another session must not be found: %v", err)
	}
	if _, err := Last(profile, "sess-none"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no page: %v", err)
	}
}

func TestRecordCapsAndRefusesBadIDs(t *testing.T) {
	profile := fmt.Sprintf("cap%d", time.Now().UnixNano())
	for i := 0; i < MaxPerSession+5; i++ {
		if _, err := Record(profile, Page{SessionID: "s", Path: fmt.Sprintf("/w/%d.html", i)}); err != nil {
			t.Fatal(err)
		}
	}
	pages, _ := List(profile, "s")
	if len(pages) != MaxPerSession {
		t.Fatalf("cap: %d", len(pages))
	}
	for _, bad := range []string{"", "../x", "a/b", "..", "-x", "a b"} {
		if _, err := Record(profile, Page{SessionID: bad, Path: "/w/x.html"}); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("session %q: %v", bad, err)
		}
		if _, err := List(profile, bad); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("list %q: %v", bad, err)
		}
	}
	for _, bad := range []string{"", "../../etc", "ABCDEF0123456789", "0123"} {
		if _, err := Get(profile, "s", bad); !errors.Is(err, ErrInvalidPage) {
			t.Fatalf("page %q: %v", bad, err)
		}
	}
	if _, err := Dir("../evil"); err == nil {
		t.Fatal("profile path traversal accepted")
	}
}
