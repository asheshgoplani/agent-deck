package events

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// KindTmuxOutput is the payload-less "pane changed" tick the tmux pipe
// manager can publish on every %output line.
const KindTmuxOutput = "tmux.output"

// DemandKinds are published only while a live follower has asked for them
// (issue #2481): nothing reads a tick with no payload unless someone is
// following it, so with no demand the producer writes nothing at all.
var DemandKinds = []string{KindTmuxOutput}

const (
	// demandDirName holds one lease file per follower and demanded kind:
	// "<kind>.<pid>.<random>". A follower in any process creates it; a
	// producer in any process sees it.
	demandDirName = "want"
	// demandRefresh is how often a live follower touches its lease;
	// demandTTL is how long a lease counts after its last touch, so a
	// follower that died without cleaning up stops counting on its own.
	demandRefresh = 5 * time.Second
	demandTTL     = 3 * demandRefresh
	// demandRecheck bounds how often a producer looks at the lease dir.
	demandRecheck = time.Second
)

// Want registers this process as a follower of the given demand kinds on b
// until the returned release is called. It is a no-op (with a no-op release)
// on a disabled or read-only bus and for kinds outside DemandKinds.
func (b *Bus) Want(kinds ...string) (release func()) {
	if b == nil || !b.enabled || b.readOnly {
		return func() {}
	}
	dir := filepath.Join(b.dir, demandDirName)
	sweepStaleLeases(dir, time.Now())
	var demanded []string
	for _, k := range kinds {
		if slices.Contains(DemandKinds, k) {
			demanded = append(demanded, k)
		}
	}
	dirMode := os.FileMode(0o755)
	if b.durable() {
		dirMode = 0o700
	}
	if len(demanded) == 0 || os.MkdirAll(dir, dirMode) != nil {
		return func() {}
	}
	pid := strconv.Itoa(os.Getpid())
	var paths []string
	for _, k := range demanded {
		p := filepath.Join(dir, k+"."+pid+"."+leaseToken())
		if os.WriteFile(p, nil, b.fileMode) == nil {
			paths = append(paths, p)
		}
	}
	// touch refreshes a lease, recreating it if another follower swept it
	// while this process's refresh was late (laptop sleep, SIGSTOP).
	touch := func(p string, now time.Time) {
		if os.Chtimes(p, now, now) == nil {
			return
		}
		if os.MkdirAll(dir, dirMode) == nil {
			_ = os.WriteFile(p, nil, b.fileMode)
		}
	}
	if len(paths) == 0 {
		return func() {}
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(demandRefresh)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				for _, p := range paths {
					touch(p, now)
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			<-done
			for _, p := range paths {
				_ = os.Remove(p)
			}
		})
	}
}

// Wants reports whether a live follower currently demands kind on b.
func (b *Bus) Wants(kind string) bool {
	if b == nil || !b.enabled {
		return false
	}
	return demandIn(b.dir, kind, time.Now())
}

var (
	demandCacheMu sync.Mutex
	demandCache   = map[string]demandEntry{}
)

type demandEntry struct {
	profile string
	checked time.Time
	wanted  bool
}

// DefaultWants reports whether a live follower of the process's default bus
// demands kind. It never opens the bus and resolves and reads the lease dir
// at most once per demandRecheck, so a hot producer can call it on every
// event.
func DefaultWants(kind string) bool {
	if disabled, explicit := envOverride(); explicit && disabled {
		return false
	}
	profile := CurrentProfile()
	now := time.Now()
	demandCacheMu.Lock()
	defer demandCacheMu.Unlock()
	if e, ok := demandCache[kind]; ok && e.profile == profile && now.Sub(e.checked) < demandRecheck {
		return e.wanted
	}
	wanted := false
	if dir, err := busDirFor(profile); err == nil {
		wanted = demandIn(dir, kind, now)
	}
	demandCache[kind] = demandEntry{profile: profile, checked: now, wanted: wanted}
	return wanted
}

func demandIn(busDir, kind string, now time.Time) bool {
	entries, err := os.ReadDir(filepath.Join(busDir, demandDirName))
	if err != nil {
		return false
	}
	prefix := kind + "."
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) < demandTTL {
			return true
		}
	}
	return false
}

// sweepStaleLeases removes leases a crashed follower left behind.
func sweepStaleLeases(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) >= demandTTL {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func leaseToken() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}
