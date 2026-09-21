package update

// Stall-tolerant release download. A total http.Client.Timeout makes any link
// slower than size/timeout unable to ever update. Here the connection phases
// have their own limits, the body only fails when no bytes arrive for
// StallTimeout, a broken body is resumed with a Range request, and one overall
// cap (plus the caller's context) bounds the whole thing.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// downloadTuning holds every timeout of one download so tests can shrink them.
type downloadTuning struct {
	ConnectTimeout time.Duration // dial, TLS handshake and response headers
	StallTimeout   time.Duration // max silence while reading the body
	Overall        time.Duration // cap for all attempts together
	Retries        int           // extra attempts after the first
	Backoff        time.Duration // base wait before a retry (grows per attempt)
}

// archiveTuning is used for the release archive (~15 MB).
var archiveTuning = downloadTuning{
	ConnectTimeout: 20 * time.Second,
	StallTimeout:   30 * time.Second,
	Overall:        30 * time.Minute,
	Retries:        3,
	Backoff:        2 * time.Second,
}

// checksumsTuning is used for the tiny checksums.txt.
var checksumsTuning = downloadTuning{
	ConnectTimeout: 20 * time.Second,
	StallTimeout:   30 * time.Second,
	Overall:        2 * time.Minute,
	Retries:        3,
	Backoff:        2 * time.Second,
}

// downloadProgress, when non-nil, receives one self-overwriting progress line.
// PerformVerifiedUpdate sets it only when stdout is a terminal.
var downloadProgress io.Writer

// DownloadError reports a download that gave up, with what was received.
type DownloadError struct {
	Received, Total int64 // Total is -1 when unknown
	Attempts        int
	Stalled         bool
	Err             error
}

func (e *DownloadError) Error() string {
	what := "the connection broke"
	if e.Stalled {
		what = "no data arrived for a while (the connection stalled)"
	}
	got := fmt.Sprintf("%.1f MB", float64(e.Received)/1e6)
	if e.Total > 0 {
		got += fmt.Sprintf(" of %.1f MB", float64(e.Total)/1e6)
	}
	return fmt.Sprintf("download gave up after %d attempts: %s; received %s (%v). Check your connection and run `agent-deck update` again; the background updater keeps trying too",
		e.Attempts, what, got, e.Err)
}

func (e *DownloadError) Unwrap() error { return e.Err }

func newDownloadClient(t downloadTuning) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: t.ConnectTimeout, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = t.ConnectTimeout
	tr.ResponseHeaderTimeout = t.ConnectTimeout
	return &http.Client{Transport: tr}
}

// downloadBytes fetches url into memory, resuming after stalls or drops.
func downloadBytes(ctx context.Context, url string, t downloadTuning, progress io.Writer) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, t.Overall)
	defer cancel()
	client := newDownloadClient(t)
	defer client.CloseIdleConnections()

	var buf []byte
	total := int64(-1)
	start := time.Now()
	var lastErr error
	stalled := false
	attempts := 0
	for attempt := 0; attempt <= t.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(t.Backoff * time.Duration(attempt)):
			}
		}
		attempts++
		var retry bool
		var err error
		buf, total, stalled, retry, err = fetchOnce(ctx, client, url, t, buf, total, start, progress)
		if err == nil {
			if progress != nil {
				fmt.Fprintln(progress)
			}
			return buf, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	if progress != nil {
		fmt.Fprintln(progress)
	}
	return nil, &DownloadError{Received: int64(len(buf)), Total: total, Attempts: attempts, Stalled: stalled, Err: lastErr}
}

// fetchOnce runs one request, appending to buf. retry says whether a failure
// is worth another attempt.
func fetchOnce(ctx context.Context, client *http.Client, url string, t downloadTuning, buf []byte, total int64, start time.Time, progress io.Writer) (out []byte, outTotal int64, stalled, retry bool, err error) {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return buf, total, false, false, err
	}
	if len(buf) > 0 {
		req.Header.Set("Range", "bytes="+strconv.Itoa(len(buf))+"-")
	}
	resp, err := client.Do(req)
	if err != nil {
		return buf, total, false, true, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusPartialContent && len(buf) > 0 && contentRangeStart(resp) == int64(len(buf)):
		if n := resp.ContentLength; n >= 0 {
			total = int64(len(buf)) + n
		}
	case resp.StatusCode == http.StatusOK:
		// First attempt, or the server ignored Range: start clean.
		buf = buf[:0]
		total = resp.ContentLength
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable, resp.StatusCode == http.StatusPartialContent:
		buf = buf[:0]
		return buf, -1, false, true, fmt.Errorf("server rejected resume (status %d)", resp.StatusCode)
	default:
		retryable := resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests
		return buf, total, false, retryable, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	var stall atomic.Bool
	timer := time.AfterFunc(t.StallTimeout, func() { stall.Store(true); cancel() })
	defer timer.Stop()
	chunk := make([]byte, 32*1024)
	lastDraw := time.Time{}
	for {
		n, rerr := resp.Body.Read(chunk)
		if n > 0 {
			timer.Reset(t.StallTimeout)
			buf = append(buf, chunk[:n]...)
			if progress != nil && time.Since(lastDraw) > 250*time.Millisecond {
				lastDraw = time.Now()
				drawProgress(progress, int64(len(buf)), total, start)
			}
		}
		if rerr == io.EOF {
			if total > 0 && int64(len(buf)) < total {
				return buf, total, false, true, io.ErrUnexpectedEOF
			}
			if progress != nil {
				drawProgress(progress, int64(len(buf)), total, start)
			}
			return buf, total, false, false, nil
		}
		if rerr != nil {
			if stall.Load() && ctx.Err() == nil {
				return buf, total, true, true, fmt.Errorf("no data for %s", t.StallTimeout)
			}
			return buf, total, false, true, rerr
		}
	}
}

// contentRangeStart parses the first byte of "bytes 100-199/200"; -1 if absent.
func contentRangeStart(resp *http.Response) int64 {
	v := strings.TrimPrefix(resp.Header.Get("Content-Range"), "bytes ")
	dash := strings.IndexByte(v, '-')
	if dash < 0 {
		return -1
	}
	n, err := strconv.ParseInt(v[:dash], 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func drawProgress(w io.Writer, got, total int64, start time.Time) {
	rate := 0.0
	if s := time.Since(start).Seconds(); s > 0 {
		rate = float64(got) / 1e6 / s
	}
	if total > 0 {
		fmt.Fprintf(w, "\r  %.1f / %.1f MB  %.2f MB/s   ", float64(got)/1e6, float64(total)/1e6, rate)
		return
	}
	fmt.Fprintf(w, "\r  %.1f MB  %.2f MB/s   ", float64(got)/1e6, rate)
}

// terminalProgress returns stdout when it is a terminal, else nil.
func terminalProgress() io.Writer {
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return os.Stdout
	}
	return nil
}
