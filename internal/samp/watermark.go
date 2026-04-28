package samp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Watermark is the persisted reader state from SPEC.md §6 step 2.
type Watermark struct {
	TS  int64    `json:"ts"`
	IDs []string `json:"ids"`
}

// MtimeCache is the optional reader perf cache (SPEC.md §6 step 1, step 7).
type MtimeCache struct {
	MaxMtime int64 `json:"max_mtime"`
	Files    int   `json:"files"`
}

// LoadWatermark reads $DIR/.seen-<me>; returns (nil, nil) when absent.
func LoadWatermark(dir, me string) (*Watermark, error) {
	if err := ValidateAlias(me); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, ".seen-"+me))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("LoadWatermark: %w", err)
	}
	var wm Watermark
	if err := json.Unmarshal(b, &wm); err != nil {
		return nil, fmt.Errorf("LoadWatermark: parse: %w", err)
	}
	return &wm, nil
}

// SaveWatermark writes $DIR/.seen-<me> atomically (tempfile + rename
// in the same directory, so the rename is POSIX-atomic).
func SaveWatermark(dir, me string, wm *Watermark) error {
	if err := ValidateAlias(me); err != nil {
		return err
	}
	b, err := json.Marshal(wm)
	if err != nil {
		return fmt.Errorf("SaveWatermark: marshal: %w", err)
	}
	return atomicWrite(dir, ".seen-"+me, b)
}

// LoadMtimeCache reads $DIR/.mtime-<me>; returns (nil, nil) when absent.
func LoadMtimeCache(dir, me string) (*MtimeCache, error) {
	if err := ValidateAlias(me); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, ".mtime-"+me))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("LoadMtimeCache: %w", err)
	}
	var c MtimeCache
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("LoadMtimeCache: parse: %w", err)
	}
	return &c, nil
}

// SaveMtimeCache writes $DIR/.mtime-<me> atomically.
func SaveMtimeCache(dir, me string, c *MtimeCache) error {
	if err := ValidateAlias(me); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("SaveMtimeCache: marshal: %w", err)
	}
	return atomicWrite(dir, ".mtime-"+me, b)
}

// CurrentMtime stats $DIR/log-*.jsonl and returns (max_mtime_unix,
// file_count). Used by readers to short-circuit Scan when nothing
// observably changed (SPEC.md §6 step 1). Returns (0, 0) when no log
// files exist.
func CurrentMtime(dir string) (int64, int, error) {
	logs, err := filepath.Glob(filepath.Join(dir, "log-*.jsonl"))
	if err != nil {
		return 0, 0, fmt.Errorf("CurrentMtime: glob: %w", err)
	}
	var max int64
	for _, p := range logs {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if t := fi.ModTime().Unix(); t > max {
			max = t
		}
	}
	return max, len(logs), nil
}

// atomicWrite writes data to dir/name via tempfile + rename. The tempfile
// lives in dir so the rename stays on the same filesystem (POSIX atomic).
func atomicWrite(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("atomicWrite: mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return fmt.Errorf("atomicWrite: tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("atomicWrite: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("atomicWrite: close: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, name)); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("atomicWrite: rename: %w", err)
	}
	return nil
}
