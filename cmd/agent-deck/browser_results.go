package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/macapp"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Large browser results (page text, screenshots) stay out of the shared
// events log: `events publish --kind macapp.browser.response` writes a result
// over macapp.SpillThreshold to a 0600 side file in a 0700 directory and
// publishes a result_ref instead; the waiting `agent-deck browser` reads the
// file and deletes it. Files older than macapp.ResultTTL are swept on every
// publish and every browser command.

// browserResultsDir is <data dir>/runtime/browser-results/<profile>.
func browserResultsDir(profile string) (string, error) {
	p, err := session.ResolveProfileForStorage(profile)
	if err != nil {
		return "", err
	}
	if p == "" || filepath.Base(p) != p || !filepath.IsLocal(p) {
		return "", fmt.Errorf("invalid profile %q", p)
	}
	return agentpaths.EffectiveDataPath(filepath.Join("runtime", "browser-results", p), "runtime")
}

// ensurePrivateDir creates dir 0700 and tightens it if it already existed.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// spillBrowserResponse moves a large result of a browser response payload to
// a side file and returns the payload with a result_ref in its place. A small
// result, or a payload that is not a JSON object, is returned unchanged.
func spillBrowserResponse(profile string, raw []byte, now time.Time) ([]byte, error) {
	dir, err := browserResultsDir(profile)
	if err != nil {
		return nil, err
	}
	sweepBrowserResults(dir, now)
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return raw, nil
	}
	result, ok := fields["result"]
	if !ok || len(result) <= macapp.SpillThreshold {
		return raw, nil
	}
	var reqID string
	_ = json.Unmarshal(fields["req_id"], &reqID)
	if !macapp.ValidReqID(reqID) {
		if reqID, err = newBrowserReqID(); err != nil {
			return nil, err
		}
	}
	if err := ensurePrivateDir(dir); err != nil {
		return nil, err
	}
	name := reqID + ".json"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, result, 0o600); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(result)
	ref, err := json.Marshal(macapp.ResultRef{File: name, Bytes: len(result), SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		return nil, err
	}
	delete(fields, "result")
	fields["result_ref"] = ref
	return json.Marshal(fields)
}

// sweepBrowserResults removes side files older than macapp.ResultTTL.
func sweepBrowserResults(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > macapp.ResultTTL {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

// maxSpilledResult bounds what the CLI reads back (the remote reply cap,
// with room for a result published on the host itself).
const maxSpilledResult = 4 << 20

// loadBrowserResult reads a spilled result back, checks its size and digest,
// and deletes the file.
func loadBrowserResult(profile string, ref *macapp.ResultRef) (json.RawMessage, error) {
	if ref == nil || !strings.HasSuffix(ref.File, ".json") || !macapp.ValidReqID(strings.TrimSuffix(ref.File, ".json")) {
		return nil, errors.New("result_ref names no result file")
	}
	dir, err := browserResultsDir(profile)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ref.File)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("spilled result: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxSpilledResult+1))
	_ = f.Close()
	_ = os.Remove(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if len(raw) > maxSpilledResult || len(raw) != ref.Bytes || hex.EncodeToString(sum[:]) != ref.SHA256 {
		return nil, errors.New("spilled result does not match its result_ref")
	}
	return raw, nil
}
