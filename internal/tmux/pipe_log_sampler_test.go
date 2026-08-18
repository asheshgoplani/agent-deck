package tmux

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func sampleClock(start time.Time) (*time.Time, func() time.Time) {
	now := start
	return &now, func() time.Time { return now }
}

// The pathology: one session whose instance record outlived its tmux session
// wrote pipe_closed / pipe_reader_exited / pipe_connect_retry / pipe_scanner_error
// at ~63 lines/sec — ~90% of a 32MB, 45-minute debug log. Unsampled, 60s of
// that is ~3800 lines for a single event; sampled it must be 1.
func TestPipeEventSampler_CapsAPathologicalSession(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now, clock := sampleClock(start)
	s := newPipeEventSampler(clock)

	const rate = 63 // observed lines/sec for one session
	emitted := 0
	for i := 0; i < 60*rate; i++ {
		if emit, _ := s.sample("pipe_closed", "ghost"); emit {
			emitted++
		}
		*now = now.Add(time.Second / rate)
	}
	if emitted != 1 {
		t.Fatalf("emitted %d lines for %d occurrences inside one %v window, want 1",
			emitted, 60*rate, pipeEventSampleWindow)
	}
	t.Logf("occurrences=%d emitted=%d (window=%v)", 60*rate, emitted, pipeEventSampleWindow)
}

// Sampling must not silently drop the signal: the first line after the window
// closes carries the count of what was suppressed.
func TestPipeEventSampler_ReportsSuppressedCount(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now, clock := sampleClock(start)
	s := newPipeEventSampler(clock)

	if emit, dropped := s.sample("pipe_closed", "ghost"); !emit || dropped != 0 {
		t.Fatalf("first occurrence = (emit=%v, dropped=%d), want (true, 0)", emit, dropped)
	}
	for i := 0; i < 99; i++ {
		if emit, _ := s.sample("pipe_closed", "ghost"); emit {
			t.Fatalf("occurrence %d inside the window must be suppressed", i+2)
		}
	}
	*now = now.Add(pipeEventSampleWindow)
	emit, dropped := s.sample("pipe_closed", "ghost")
	if !emit {
		t.Fatal("the first occurrence after the window closes must be emitted")
	}
	if dropped != 99 {
		t.Fatalf("suppressed count = %d, want 99", dropped)
	}
}

// Suppression is per (event, session): a storm on one session must not hide a
// different session's first pipe failure, which is what made the storm
// diagnosable in the first place.
func TestPipeEventSampler_KeyedPerEventAndSession(t *testing.T) {
	_, clock := sampleClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s := newPipeEventSampler(clock)

	s.sample("pipe_closed", "noisy")
	if emit, _ := s.sample("pipe_closed", "quiet"); !emit {
		t.Fatal("a different session's first occurrence must be emitted")
	}
	if emit, _ := s.sample("pipe_scanner_error", "noisy"); !emit {
		t.Fatal("a different event on the same session must be emitted")
	}
	if emit, _ := s.sample("pipe_closed", "noisy"); emit {
		t.Fatal("the noisy pair must still be suppressed")
	}
}

// The key space must stay bounded: a TUI that churns through session names for
// days cannot be allowed to grow the sampler without limit.
func TestPipeEventSampler_KeySpaceStaysBounded(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now, clock := sampleClock(start)
	s := newPipeEventSampler(clock)

	for i := 0; i < pipeEventSamplerCapacity*3; i++ {
		s.sample("pipe_closed", "session-"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+time.Duration(i).String())
		*now = now.Add(time.Second)
	}
	s.mu.Lock()
	size := len(s.states)
	s.mu.Unlock()
	if size > pipeEventSamplerCapacity {
		t.Fatalf("sampler holds %d keys, want <= %d", size, pipeEventSamplerCapacity)
	}
}

// Structural guard: these four events are the ones that flooded the log, so
// every one of their log sites must sit behind the sampler. A future bare
// pipeLog.Debug("pipe_closed", ...) would silently restore the firehose.
func TestPipeEventLogSitesAreSampled(t *testing.T) {
	src, err := os.ReadFile("controlpipe.go")
	if err != nil {
		t.Fatalf("read controlpipe.go: %v", err)
	}
	text := string(src)
	for _, event := range []string{"pipe_closed", "pipe_reader_exited", "pipe_connect_retry", "pipe_scanner_error"} {
		logSites := regexp.MustCompile(`pipeLog\.\w+\(\s*\n?\s*"`+event+`"`).FindAllString(text, -1)
		gates := strings.Count(text, `pipeEvents.sample("`+event+`"`)
		if len(logSites) == 0 {
			t.Errorf("%s: no log site found — did it move out of controlpipe.go?", event)
			continue
		}
		if gates != len(logSites) {
			t.Errorf("%s: %d log site(s) but %d pipeEvents.sample gate(s); every high-frequency pipe event must be sampled",
				event, len(logSites), gates)
		}
	}
}
