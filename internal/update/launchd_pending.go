package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// launchdServiceEnv is set by launchd on every process of a service and
// inherited by the service's children: it names the service an updater
// spawned by a com.agentdeck.* daemon runs inside.
const launchdServiceEnv = "XPC_SERVICE_NAME"

// PendingRebootstrapFileName is the cache-dir marker listing launch agents
// a run could not re-register because it ran inside them.
const PendingRebootstrapFileName = "launchd-rebootstrap-pending.json"

// pendingRebootstrap is the on-disk shape of the marker.
type pendingRebootstrap struct {
	Labels    []string  `json:"labels"`
	UpdatedAt time.Time `json:"updated_at"`
}

// insideLaunchdService reports whether this process runs inside the launchd
// service label (service is XPC_SERVICE_NAME, "" outside launchd).
func insideLaunchdService(service, label string) bool {
	return service != "" && service == label
}

// PendingRebootstrap returns the labels the marker at path holds, sorted;
// none when the file does not exist.
func PendingRebootstrap(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m pendingRebootstrap
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	sort.Strings(m.Labels)
	return m.Labels, nil
}

// HasPendingRebootstrap reports whether the default marker names any agent.
func HasPendingRebootstrap() bool {
	dir, err := getCacheDir()
	if err != nil {
		return false
	}
	labels, err := PendingRebootstrap(filepath.Join(dir, PendingRebootstrapFileName))
	return err == nil && len(labels) > 0
}

func addPendingRebootstrap(path, label string) error {
	return writePendingRebootstrap(path, func(labels []string) []string {
		for _, l := range labels {
			if l == label {
				return labels
			}
		}
		return append(labels, label)
	})
}

func removePendingRebootstrap(path, label string) error {
	return writePendingRebootstrap(path, func(labels []string) []string {
		out := labels[:0]
		for _, l := range labels {
			if l != label {
				out = append(out, l)
			}
		}
		return out
	})
}

func writePendingRebootstrap(path string, edit func([]string) []string) error {
	if path == "" {
		return errors.New("pending marker path unknown")
	}
	labels, err := PendingRebootstrap(path)
	if err != nil {
		return err
	}
	labels = edit(labels)
	if len(labels) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	sort.Strings(labels)
	data, err := json.MarshalIndent(pendingRebootstrap{Labels: labels, UpdatedAt: time.Now()}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o644)
}

// DrainPendingRebootstrap re-registers every agent the marker names whose
// service this process is not inside, clearing each one from the marker as
// it comes back. A label whose plist is gone is dropped from the marker and
// reported in Skipped. Not darwin, or no marker: nothing to do.
func DrainPendingRebootstrap(opts RebootstrapOptions) (RebootstrapResult, error) {
	res := RebootstrapResult{Skipped: map[string]string{}}
	if err := opts.fill(); err != nil {
		return res, err
	}
	if opts.GOOS != "darwin" {
		return res, nil
	}
	labels, err := PendingRebootstrap(opts.PendingPath)
	if err != nil || len(labels) == 0 {
		return res, err
	}
	opts.Logger.Info("launchagent_pending_drain", slog.Any("labels", labels), slog.String("pending", opts.PendingPath))
	for _, label := range labels {
		if insideLaunchdService(opts.ServiceLabel, label) {
			opts.Logger.Info("launchagent_pending_kept", slog.String("label", label), slog.String("reason", "this process runs inside it"))
			res.Deferred = append(res.Deferred, label)
			continue
		}
		path := filepath.Join(opts.LaunchAgentsDir, label+".plist")
		data, err := os.ReadFile(path)
		if err != nil {
			res.Skipped[label] = "plist unreadable: " + err.Error()
			opts.Logger.Warn("launchagent_pending_dropped", slog.String("label", label), slog.String("err", err.Error()))
			_ = removePendingRebootstrap(opts.PendingPath, label)
			continue
		}
		agent, err := ParseLaunchAgentPlist(data)
		if err != nil {
			res.Skipped[label] = "plist unparsable: " + err.Error()
			opts.Logger.Warn("launchagent_pending_dropped", slog.String("label", label), slog.String("err", err.Error()))
			_ = removePendingRebootstrap(opts.PendingPath, label)
			continue
		}
		agent.Path = path
		if err := rebootstrapOne(opts, agent); err != nil {
			fmt.Fprintf(opts.Out, "  ✗ %s: %v\n", agent.Label, err)
			return res, err
		}
		res.Restarted = append(res.Restarted, agent.Label)
		fmt.Fprintf(opts.Out, "  ↻ %s re-registered with launchd (was pending)\n", agent.Label)
		if err := removePendingRebootstrap(opts.PendingPath, label); err != nil {
			opts.Logger.Warn("launchagent_pending_clear_failed", slog.String("label", label), slog.String("err", err.Error()))
		}
	}
	return res, nil
}
