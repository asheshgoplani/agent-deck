// Package crashreport sends crash and error reports to a Sentry-compatible
// endpoint. It is OFF unless a DSN is configured at run time (the managed
// configuration or config.toml); no DSN is ever compiled in. Every event is
// rebuilt from an allow-list in BeforeSend (scrubEvent): no free text is
// sent; error messages and panic values become a 12-hex hash (MessageHash)
// next to the exception type and the stack frames; breadcrumb messages and
// data, request, user, server name, contexts, modules and extra are
// dropped. See TELEMETRY.md "Crash reports".
package crashreport

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/asheshgoplani/agent-deck/internal/managed"
)

// Config is what Init needs. An empty DSN means off.
type Config struct {
	DSN         string
	Release     string // agent-deck version
	OrgID       string // validated opaque org id, "" for none
	Environment string // "production" when empty
	Home        string // the person's home directory, scrubbed from text

	// Transport replaces the HTTP transport (tests only).
	Transport sentry.Transport
}

// flushTimeout bounds Flush at exit and after a panic.
const flushTimeout = 2 * time.Second

var (
	mu  sync.Mutex
	hub *sentry.Hub
)

// Init starts crash reporting when c.DSN is set and valid. Without a DSN it
// creates nothing (no client, no goroutine, no network) and returns false.
// It never touches sentry's global hub.
func Init(c Config) (bool, error) {
	if c.DSN == "" {
		return false, nil
	}
	if err := managed.ValidateDSN(c.DSN); err != nil {
		return false, fmt.Errorf("crash reporting not started: dsn %w", err)
	}
	if c.Environment == "" {
		c.Environment = "production"
	}
	if c.Home == "" {
		c.Home, _ = os.UserHomeDir()
	}
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:         c.DSN,
		Release:     safeRelease(c.Release),
		Environment: c.Environment,
		ServerName:  "-",
		// The SDK collects nothing on its own: no user info, cookies,
		// headers, bodies or query parameters (scrubEvent drops these as
		// well). This replaces the deprecated SendDefaultPII: false.
		DataCollection: &sentry.DataCollection{
			UserInfo: sentry.Set(false),
			Cookies:  &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
			HTTPHeaders: &sentry.HeaderCollectionConfig{
				Request:  &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
				Response: &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
			},
			HTTPBodies:  []sentry.BodyType{},
			QueryParams: &sentry.KeyValueCollectionBehavior{Mode: sentry.CollectionOff},
		},
		AttachStacktrace:     true,
		MaxBreadcrumbs:       30,
		DisableClientReports: true,
		Transport:            c.Transport,
		// No default integrations: they add modules, the environment and
		// the host's contexts, all of which scrubEvent would drop anyway.
		Integrations: func([]sentry.Integration) []sentry.Integration { return nil },
		BeforeSend: func(ev *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			return scrubEvent(ev, c)
		},
		BeforeSendTransaction: func(*sentry.Event, *sentry.EventHint) *sentry.Event { return nil },
	})
	if err != nil {
		return false, errors.New("crash reporting not started: the dsn was refused")
	}
	mu.Lock()
	defer mu.Unlock()
	if hub != nil {
		hub.Client().Close()
	}
	hub = sentry.NewHub(client, sentry.NewScope())
	return true, nil
}

// Enabled reports whether Init started a client.
func Enabled() bool {
	mu.Lock()
	defer mu.Unlock()
	return hub != nil
}

func current() *sentry.Hub {
	mu.Lock()
	defer mu.Unlock()
	return hub
}

// CaptureError reports err: its type, MessageHash of each message in its
// chain and the stack, never the message text. A no-op when off.
func CaptureError(err error) {
	if h := current(); h != nil && err != nil {
		h.CaptureException(err)
	}
}

// Breadcrumb records a category (a fixed, code-chosen name such as
// "session.start") for the next event. Only the category is ever sent.
func Breadcrumb(category string) {
	if h := current(); h != nil {
		h.AddBreadcrumb(&sentry.Breadcrumb{Category: category, Level: sentry.LevelInfo}, nil)
	}
}

// Recover reports a panic in the calling goroutine, flushes, and panics
// again with the same value, so the crash itself is unchanged. Use it as
// `defer crashreport.Recover()`; with crash reporting off it only re-panics.
func Recover() {
	r := recover()
	if r == nil {
		return
	}
	if h := current(); h != nil {
		h.Recover(r)
		h.Flush(flushTimeout)
	}
	panic(r)
}

// Flush waits up to two seconds for queued events. A no-op when off.
func Flush() {
	if h := current(); h != nil {
		h.Flush(flushTimeout)
	}
}

// Close flushes and stops the client; Enabled is false afterwards.
func Close() {
	mu.Lock()
	h := hub
	hub = nil
	mu.Unlock()
	if h != nil {
		h.Flush(flushTimeout)
		h.Client().Close()
	}
}

// tagsFor is the complete tag set of every event.
func tagsFor(c Config) map[string]string {
	t := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH}
	if c.OrgID != "" && managed.ValidOrgID(c.OrgID) {
		t["org"] = c.OrgID
	}
	return t
}
