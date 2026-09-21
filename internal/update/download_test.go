package update

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func fastTuning() downloadTuning {
	return downloadTuning{
		ConnectTimeout: 2 * time.Second,
		StallTimeout:   150 * time.Millisecond,
		Overall:        10 * time.Second,
		Retries:        3,
		Backoff:        time.Millisecond,
	}
}

func payload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

// dropAfter sends the headers for the full body, writes cut bytes, then closes
// the connection so the client sees an unexpected EOF.
func dropAfter(w http.ResponseWriter, data []byte, cut int) {
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data[:cut])
	w.(http.Flusher).Flush()
	conn, _, _ := w.(http.Hijacker).Hijack()
	_ = conn.Close()
}

func TestDownloadBytes_SlowButSteadyBeatsTotalTimeout(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	data := payload(40 * 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		for i := 0; i < len(data); i += 4096 {
			_, _ = w.Write(data[i : i+4096])
			w.(http.Flusher).Flush()
			time.Sleep(40 * time.Millisecond) // ~400ms total, far over the 150ms stall limit
		}
	}))
	defer srv.Close()
	start := time.Now()
	got, err := downloadBytes(context.Background(), srv.URL, fastTuning(), nil)
	if err != nil {
		t.Fatalf("steady slow body must succeed, got %v", err)
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatalf("test did not exceed the stall limit in total (%v)", time.Since(start))
	}
	if !bytes.Equal(got, data) {
		t.Fatal("body mismatch")
	}
}

func TestDownloadBytes_StallFailsAfterStallTimeout(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	data := payload(10000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data[:5000])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	tn := fastTuning()
	tn.Retries = 1
	start := time.Now()
	_, err := downloadBytes(context.Background(), srv.URL, tn, nil)
	var de *DownloadError
	if !errors.As(err, &de) {
		t.Fatalf("want *DownloadError, got %v", err)
	}
	if !de.Stalled || de.Attempts != 2 || de.Received != 5000 {
		t.Fatalf("unexpected error detail: %+v", de)
	}
	for _, want := range []string{"5.0 MB"[:0] + "0.0 MB of 0.0 MB", "agent-deck update", "background updater"} {
		if !strings.Contains(de.Error(), want) {
			t.Fatalf("error %q lacks %q", de.Error(), want)
		}
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("stall took %v to surface", d)
	}
}

func TestDownloadBytes_ResumesWithRangeAfterDisconnect(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	data := payload(30000)
	var calls atomic.Int32
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			dropAfter(w, data, 12000)
			return
		}
		ranges = append(ranges, r.Header.Get("Range"))
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	got, err := downloadBytes(context.Background(), srv.URL, fastTuning(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("resumed body mismatch")
	}
	if len(ranges) != 1 || ranges[0] != "bytes=12000-" {
		t.Fatalf("resume Range headers = %v, want [bytes=12000-]", ranges)
	}
}

func TestDownloadBytes_ServerIgnoringRangeRestartsClean(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	data := payload(30000)
	var calls atomic.Int32
	var sawRange atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			dropAfter(w, data, 12000)
			return
		}
		if r.Header.Get("Range") != "" {
			sawRange.Store(true)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data) // plain 200, Range ignored
	}))
	defer srv.Close()
	got, err := downloadBytes(context.Background(), srv.URL, fastTuning(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !sawRange.Load() || !bytes.Equal(got, data) {
		t.Fatalf("sawRange=%v equal=%v; a 200 after a Range request must restart cleanly without duplicated bytes", sawRange.Load(), bytes.Equal(got, data))
	}
}

func TestDownloadBytes_ContextCancelStopsPromptly(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		_, _ = w.Write([]byte("x"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	tn := fastTuning()
	tn.StallTimeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(80 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := downloadBytes(ctx, srv.URL, tn, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("cancel took %v", d)
	}
}

func TestDownloadBytes_ClientErrorIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.NotFound(w, nil)
	}))
	defer srv.Close()
	if _, err := downloadBytes(context.Background(), srv.URL, fastTuning(), nil); err == nil || calls.Load() != 1 {
		t.Fatalf("404 must fail once, err=%v calls=%d", err, calls.Load())
	}
}

func TestDownloadBytes_ProgressLine(t *testing.T) {
	data := payload(20000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(data) }))
	defer srv.Close()
	var out bytes.Buffer
	if _, err := downloadBytes(context.Background(), srv.URL, fastTuning(), &out); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "MB/s") || !strings.Contains(s, "/ 0.0 MB") {
		t.Fatalf("progress output %q lacks MB done / total and rate", s)
	}
}

// A resumed archive must still pass the strict checksum gate.
func TestDownloadVerifiedBinary_ResumedArchiveStillChecksummed(t *testing.T) {
	old := archiveTuning
	archiveTuning = fastTuning()
	defer func() { archiveTuning = old }()

	archive := makeTarGz(t, []byte("new-binary"))
	run := func(t *testing.T, sums string) ([]byte, error) {
		var calls atomic.Int32
		mux := http.NewServeMux()
		mux.HandleFunc("/agent-deck_1.2.3_linux_amd64.tar.gz", func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				dropAfter(w, archive, len(archive)/2)
				return
			}
			http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(archive))
		})
		mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sums)) })
		srv := httptest.NewServer(mux)
		defer srv.Close()
		rel := &Release{TagName: "v1.2.3", Assets: []Asset{
			{Name: "agent-deck_1.2.3_linux_amd64.tar.gz", BrowserDownloadURL: srv.URL + "/agent-deck_1.2.3_linux_amd64.tar.gz"},
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
		}}
		return DownloadVerifiedBinary(rel, "linux", "amd64")
	}
	good := sha256hex(archive) + "  agent-deck_1.2.3_linux_amd64.tar.gz\n"
	if got, err := run(t, good); err != nil || string(got) != "new-binary" {
		t.Fatalf("resumed + matching checksum should install: %q %v", got, err)
	}
	bad := sha256hex([]byte("other")) + "  agent-deck_1.2.3_linux_amd64.tar.gz\n"
	if got, err := run(t, bad); err == nil || got != nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("resumed + wrong checksum must be refused: %q %v", got, err)
	}
}
