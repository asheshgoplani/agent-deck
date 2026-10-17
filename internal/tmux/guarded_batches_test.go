package tmux

import (
	"errors"
	"strings"
	"testing"
)

func TestGuardedSendRechecksBeforeEnter(t *testing.T) {
	calls := recordKeySender(t)
	s := &Session{Name: "guarded-before-enter"}
	menu := errors.New("menu opened")
	checks := 0
	before := func() error {
		checks++
		if checks == 2 {
			return menu
		}
		return nil
	}
	err := s.SendKeysAndEnterCheckedGuarded("hello", nil, nil, before)
	if !errors.Is(err, menu) || checks != 2 {
		t.Fatalf("err=%v checks=%d", err, checks)
	}
	for _, call := range *calls {
		if sentKey(call) == "Enter" {
			t.Fatalf("Enter reached menu: %v", *calls)
		}
	}
}

func TestGuardedFallbackChecksEveryChunk(t *testing.T) {
	calls := recordKeySender(t)
	s := &Session{Name: "guarded-fallback"}
	menu := errors.New("menu opened")
	checks := 0
	err := s.sendKeysChunkedFallbackChecked(s.Name, strings.Repeat("a", 9000), func() error {
		checks++
		if checks == 2 {
			return menu
		}
		return nil
	})
	if !errors.Is(err, menu) || checks != 2 || len(*calls) != 1 {
		t.Fatalf("err=%v checks=%d calls=%v", err, checks, *calls)
	}
}

func TestGuardedVimChecksBeforeEachKey(t *testing.T) {
	calls := recordKeySender(t)
	s := &Session{Name: "guarded-vim", VimMode: true}
	menu := errors.New("menu opened")
	checks := 0
	err := s.SendEnterChecked(func() error {
		checks++
		if checks == 2 {
			return menu
		}
		return nil
	})
	if !errors.Is(err, menu) || checks != 2 || len(*calls) != 1 {
		t.Fatalf("err=%v checks=%d calls=%v", err, checks, *calls)
	}
}
