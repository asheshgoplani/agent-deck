// release_readiness_update_test.go runs the update and auto-install paths of
// the release candidate end to end before a tag, in a throwaway HOME.
//
// Why: the auto-update rollout needed three follow-up fixes after it shipped
// (#2245, #2252, #2271), and several releases were tagged within hours of the
// previous one to repair it. Every one of those was found only after users
// ran the release. This test builds the candidate twice (once at its own
// Version, once at an older version), serves the candidate as a fake GitHub
// release from a local HTTP server (release JSON, goreleaser-style tarball,
// checksums.txt), and lets the older build update itself through the real
// commands:
//
//   - manual: `agent-deck update`, answering the install prompt with "y";
//   - auto-install: update.RunUnattendedInstall, the exact call the TUI and
//     the web daemon make when auto_install fires
//     (`agent-deck update --unattended --trigger tui`).
//
// After each run the binary on disk must be byte-identical to the candidate,
// must run and report the candidate version, auto-update.log must carry the
// install line, and nothing may change outside the temp HOME. Real network
// access is routed to a local proxy that refuses every connection, so the
// run never reaches GitHub. scripts/release-readiness.sh runs this test
// before a tag; it also runs in the normal suite.
package releasetests

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

const readinessRepo = "asheshgoplani/agent-deck"

func TestReleaseReadiness_UpdatePath(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("release builds exist only for linux and darwin")
	}
	root := repoRoot(t)
	candidate := candidateVersion(t, root)
	older := olderVersion(t, candidate)

	work := t.TempDir()
	candBin := filepath.Join(work, "build", "candidate", "agent-deck")
	oldBin := filepath.Join(work, "build", "older", "agent-deck")
	buildAgentDeck(t, root, candBin, candidate)
	buildAgentDeck(t, root, oldBin, older)
	candBytes, err := os.ReadFile(candBin)
	if err != nil {
		t.Fatal(err)
	}
	rel := newFakeRelease(t, candidate, candBytes)
	proxy := newRefusingProxy(t)
	parentHome := os.Getenv("HOME")

	for _, tc := range []struct {
		name, source string
		run          func(t *testing.T, bin string, env []string) string
	}{
		{"manual", "manual", runManualUpdate},
		{"auto-install", "tui", runAutoInstall},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := fingerprintAgentDeckDirs(t, parentHome)
			rel.resetRequests()
			proxy.reset()

			box := t.TempDir()
			home := filepath.Join(box, "home")
			binDir := filepath.Join(box, "bin")
			for _, d := range []string{home, binDir, filepath.Join(box, "tmp"), filepath.Join(box, "run"), filepath.Join(box, "tmux")} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeUpdateConfig(t, home)
			bin := filepath.Join(binDir, "agent-deck")
			copyExecutable(t, oldBin, bin)
			env := readinessEnv(box, home, rel.srv.URL, proxy.url())

			out := tc.run(t, bin, env)
			t.Logf("%s v%s -> v%s output:\n%s", tc.name, older, candidate, out)
			// The update went through the fake release only.
			rel.requireRequests(t, candidate)
			// No connection tried to leave the box, except the changelog
			// fetch of the interactive path (best effort, it fails here).
			for _, host := range proxy.hosts() {
				if !(tc.name == "manual" && host == "raw.githubusercontent.com:443") {
					t.Errorf("the update tried to reach %s through the network", host)
				}
			}
			// Nothing changed in the agent-deck dirs of the HOME the test
			// started with (checked first: a leak also misplaces the log).
			if after := fingerprintAgentDeckDirs(t, parentHome); after != before {
				t.Errorf("the update changed files outside the temp HOME:\nbefore:\n%s\nafter:\n%s", before, after)
			}

			// The binary on disk is now exactly the candidate.
			got, err := os.ReadFile(bin)
			if err != nil {
				t.Fatalf("installed binary missing after update: %v\noutput:\n%s", err, out)
			}
			if sha256Hex(got) != sha256Hex(candBytes) {
				t.Fatalf("binary at %s is not the candidate after the update (sha %s, want %s)\noutput:\n%s", bin, sha256Hex(got)[:12], sha256Hex(candBytes)[:12], out)
			}
			// It runs and reports the candidate version.
			if v := binaryVersion(t, bin, env); v != candidate {
				t.Fatalf("swapped binary reports version %q, want %q\noutput:\n%s", v, candidate, out)
			}
			// The swap left nothing beside it (.new, .old).
			if names := dirNames(t, binDir); len(names) != 1 || names[0] != "agent-deck" {
				t.Fatalf("install dir holds %v after the update, want only agent-deck", names)
			}
			// One install line in auto-update.log, naming the path.
			logPath := filepath.Join(home, ".local", "share", "agent-deck", "logs", update.TimerLogFileName)
			wantLine := fmt.Sprintf(" install %s -> %s ok source=%s ", older, candidate, tc.source)
			logData, err := os.ReadFile(logPath)
			if err != nil || !strings.Contains(string(logData), wantLine) {
				t.Fatalf("auto-update.log at %s lacks %q (err %v):\n%s\noutput:\n%s", logPath, wantLine, err, logData, out)
			}
		})
	}
}

// runManualUpdate is a person running `agent-deck update` and answering "y".
func runManualUpdate(t *testing.T, bin string, env []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "update")
	cmd.Env = env
	cmd.Stdin = strings.NewReader("y\n")
	cmd.Dir = filepath.Dir(bin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("agent-deck update failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Updated to v") {
		t.Fatalf("agent-deck update did not report an install:\n%s", out)
	}
	return string(out)
}

// runAutoInstall is the TUI's auto_install firing: the same call, with the
// same environment handling, that the TUI and the web daemon make.
func runAutoInstall(t *testing.T, bin string, env []string) string {
	t.Helper()
	useProcessEnv(t, env)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	out, err := update.RunUnattendedInstall(ctx, bin, "tui")
	if err != nil {
		t.Fatalf("unattended install failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "(unattended)") {
		t.Fatalf("unattended install did not report an install:\n%s", out)
	}
	return out
}

// candidateVersion is the Version the release bump put in main.go.
func candidateVersion(t *testing.T, root string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(root, "cmd", "agent-deck", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^var Version = "(\d+\.\d+\.\d+)"`).FindSubmatch(src)
	if m == nil {
		t.Fatal(`no var Version = "X.Y.Z" in cmd/agent-deck/main.go`)
	}
	return string(m[1])
}

// olderVersion is the release just before v: patch-1, else the previous minor.
func olderVersion(t *testing.T, v string) string {
	t.Helper()
	parts := strings.Split(v, ".")
	n := make([]int, 3)
	for i := range parts {
		x, err := strconv.Atoi(parts[i])
		if err != nil {
			t.Fatalf("version %q: %v", v, err)
		}
		n[i] = x
	}
	switch {
	case n[2] > 0:
		n[2]--
	case n[1] > 0:
		n[1], n[2] = n[1]-1, 99
	case n[0] > 0:
		n[0], n[1], n[2] = n[0]-1, 99, 99
	default:
		t.Fatalf("no version below %q", v)
	}
	return fmt.Sprintf("%d.%d.%d", n[0], n[1], n[2])
}

// buildAgentDeck builds ./cmd/agent-deck the way .goreleaser.yml does.
func buildAgentDeck(t *testing.T, root, out, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-ldflags", "-s -w -X main.Version="+version, "-o", out, "./cmd/agent-deck")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build v%s: %v\n%s", version, err, b)
	}
}

// fakeRelease serves one release the way GitHub does: the API documents the
// updater reads and the assets goreleaser uploads.
type fakeRelease struct {
	srv     *httptest.Server
	mu      sync.Mutex
	paths   []string
	archive string
}

func newFakeRelease(t *testing.T, version string, binary []byte) *fakeRelease {
	t.Helper()
	archiveName := fmt.Sprintf("agent-deck_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	tarball := goreleaserTarball(t, binary)
	checksums := fmt.Sprintf("%s  %s\n", sha256Hex(tarball), archiveName)

	r := &fakeRelease{archive: archiveName}
	mux := http.NewServeMux()
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.Path)
		r.mu.Unlock()
		mux.ServeHTTP(w, req)
	}))
	t.Cleanup(r.srv.Close)

	release := map[string]any{
		"tag_name":     "v" + version,
		"name":         "v" + version,
		"html_url":     "https://github.com/" + readinessRepo + "/releases/tag/v" + version,
		"published_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
		"draft":        false,
		"prerelease":   false,
		"assets": []map[string]any{
			{"name": archiveName, "browser_download_url": r.srv.URL + "/download/" + archiveName, "size": len(tarball)},
			{"name": "checksums.txt", "browser_download_url": r.srv.URL + "/download/checksums.txt", "size": len(checksums)},
		},
	}
	writeJSON := func(v any) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	api := "/repos/" + readinessRepo + "/releases"
	mux.HandleFunc(api+"/latest", writeJSON(release))
	mux.HandleFunc(api+"/tags/v"+version, writeJSON(release))
	mux.HandleFunc(api, writeJSON([]any{release}))
	mux.HandleFunc("/download/"+archiveName, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(tarball)))
		_, _ = w.Write(tarball)
	})
	mux.HandleFunc("/download/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(checksums))
	})
	return r
}

func (r *fakeRelease) resetRequests() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = nil
}

// requireRequests checks that the updater fetched the release, the tarball
// and checksums.txt, and asked for nothing the fake does not serve.
func (r *fakeRelease) requireRequests(t *testing.T, version string) {
	t.Helper()
	r.mu.Lock()
	paths := append([]string(nil), r.paths...)
	r.mu.Unlock()
	api := "/repos/" + readinessRepo + "/releases"
	known := map[string]bool{
		api + "/latest": true, api + "/tags/v" + version: true, api: true,
		"/download/" + r.archive: true, "/download/checksums.txt": true,
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if !known[p] {
			t.Errorf("updater asked the release server for unexpected path %s", p)
		}
		seen[p] = true
	}
	for _, p := range []string{api + "/latest", "/download/" + r.archive, "/download/checksums.txt"} {
		if !seen[p] {
			t.Errorf("updater never fetched %s (requests: %v)", p, paths)
		}
	}
}

// goreleaserTarball packs the binary like the release archives: the
// agent-deck binary at the root next to the docs.
func goreleaserTarball(t *testing.T, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name string, mode int64, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	add("LICENSE", 0o644, []byte("license\n"))
	add("README.md", 0o644, []byte("readme\n"))
	add("agent-deck", 0o755, binary)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// refusingProxy stands in for the internet: HTTP(S)_PROXY points at it, it
// records the host of every connection attempt and closes it. Go never
// proxies loopback, so the fake release server is still reached directly.
type refusingProxy struct {
	ln   net.Listener
	mu   sync.Mutex
	seen []string
}

func newRefusingProxy(t *testing.T) *refusingProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &refusingProxy{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			line, _ := bufio.NewReader(c).ReadString('\n')
			host := "unknown"
			if f := strings.Fields(line); len(f) >= 2 {
				host = f[1]
				if f[0] != "CONNECT" {
					host = strings.TrimPrefix(strings.TrimPrefix(host, "http://"), "https://")
				}
			}
			p.mu.Lock()
			p.seen = append(p.seen, host)
			p.mu.Unlock()
			_ = c.Close()
		}
	}()
	return p
}

func (p *refusingProxy) url() string { return "http://" + p.ln.Addr().String() }

func (p *refusingProxy) reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seen = nil
}

func (p *refusingProxy) hosts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...)
}

// readinessEnv is the environment of the binaries under test: the caller's
// PATH and locale, with HOME, every XDG dir, tmux, tmp and the systemd
// runtime dir inside box, the release API pointed at the fake, and the
// network pointed at the refusing proxy. AGENTDECK_TEST_RELEASE_READINESS
// keeps the automatic paths quiet (no timer install, no self-restart); the
// explicit update commands under test still run.
func readinessEnv(box, home, apiURL, proxyURL string) []string {
	drop := []string{"HOME=", "XDG_", "AGENTDECK_", "AGENT_DECK_", "TMUX", "GITHUB_TOKEN=", "GH_", "HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=", "http_proxy=", "https_proxy=", "no_proxy=", "ALL_PROXY=", "all_proxy=", "DBUS_SESSION_BUS_ADDRESS=", "TMPDIR=", "CLAUDE_", "CODEX_HOME=", "TELEGRAM_"}
	var env []string
next:
	for _, kv := range os.Environ() {
		for _, p := range drop {
			if strings.HasPrefix(kv, p) {
				continue next
			}
		}
		env = append(env, kv)
	}
	return append(env,
		"HOME="+home,
		"TMPDIR="+filepath.Join(box, "tmp"),
		"TMUX_TMPDIR="+filepath.Join(box, "tmux"),
		"XDG_RUNTIME_DIR="+filepath.Join(box, "run"),
		"AGENTDECK_GITHUB_API_BASE_URL="+apiURL,
		"AGENTDECK_TEST_RELEASE_READINESS=1",
		"GITHUB_TOKEN=release-readiness-fake-token",
		"HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL,
		"http_proxy="+proxyURL, "https_proxy="+proxyURL,
	)
}

// useProcessEnv makes env this process's environment for the rest of the
// test (update.RunUnattendedInstall hands its child os.Environ, as the TUI
// does), and restores the original afterwards.
func useProcessEnv(t *testing.T, env []string) {
	t.Helper()
	orig := os.Environ()
	t.Cleanup(func() {
		os.Clearenv()
		for _, kv := range orig {
			k, v, _ := strings.Cut(kv, "=")
			_ = os.Setenv(k, v)
		}
	})
	os.Clearenv()
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if err := os.Setenv(k, v); err != nil {
			t.Fatal(err)
		}
	}
}

// writeUpdateConfig turns auto_install on and the timer management off in
// the temp HOME's config.toml.
func writeUpdateConfig(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[updates]\nauto_install = true\nauto_restart = false\ncheck_enabled = true\nmanage_timer = false\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o755); err != nil {
		t.Fatal(err)
	}
}

func binaryVersion(t *testing.T, bin string, env []string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "version")
	cmd.Env = append(append([]string(nil), env...), update.SkipUpdateCheckEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s version: %v\n%s", bin, err, out)
	}
	return update.ParseVersionOutput(string(out))
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// updateWrittenFiles are the files an install writes beside the binary:
// the audit logs, the update cache and the install lock, plus the user
// timer and launch agent units.
var updateWrittenFiles = map[string]bool{
	update.TimerLogFileName: true, update.AuditLogFileName: true,
	update.CacheFileName: true, update.UpdateLockFileName: true,
}

// fingerprintAgentDeckDirs lists path, size and mtime of the files an
// update writes, in the agent-deck dirs (and the user timer and launch
// agent dirs) of the HOME the test started with and of the passwd home.
// It only reads. Only update files count, so a live deck on a developer
// box writing its other logs does not make the test flaky.
func fingerprintAgentDeckDirs(t *testing.T, home string) string {
	t.Helper()
	homes := []string{home}
	if u, err := user.Current(); err == nil && u.HomeDir != "" && u.HomeDir != home {
		homes = append(homes, u.HomeDir)
	}
	var lines []string
	for _, h := range homes {
		if h == "" {
			continue
		}
		for _, rel := range []string{".agent-deck", ".config/agent-deck", ".cache/agent-deck", ".local/share/agent-deck", ".local/state/agent-deck", ".config/systemd/user", "Library/LaunchAgents"} {
			units := rel == ".config/systemd/user" || rel == "Library/LaunchAgents"
			_ = filepath.WalkDir(filepath.Join(h, rel), func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				if !updateWrittenFiles[d.Name()] && !(units && strings.Contains(d.Name(), "agent")) {
					return nil
				}
				if info, ierr := d.Info(); ierr == nil {
					lines = append(lines, fmt.Sprintf("%s %d %d", p, info.Size(), info.ModTime().UnixNano()))
				}
				return nil
			})
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
