// Issue #1146: lefthook pre-push ran `css-verify` and `lint` in parallel.
// `make css-verify` writes a transient `internal/web/static/.brute-tw.src.css`
// during its run; `golangci-lint` snapshots `//go:embed static/*` and ~30%
// of pre-push runs caught the transient file mid-flight, failing lint.
//
// The structural fix is config-only: `pre-push` must serialize its commands
// so `lint` never starts while `css-verify` is touching `internal/web/static/`.
// This test pins that invariant so a future refactor cannot silently flip
// `pre-push` back to parallel and re-introduce the race.
package releasetests

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestIssue1146LefthookPrePushIsSerial(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "lefthook.yml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	var cfg struct {
		PrePush struct {
			Parallel bool           `yaml:"parallel"`
			Piped    bool           `yaml:"piped"`
			Commands map[string]any `yaml:"commands"`
		} `yaml:"pre-push"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	if _, ok := cfg.PrePush.Commands["css-verify"]; !ok {
		t.Fatal("pre-push.commands.css-verify missing — invariant relies on it")
	}
	if _, ok := cfg.PrePush.Commands["lint"]; !ok {
		t.Fatal("pre-push.commands.lint missing — invariant relies on it")
	}

	if cfg.PrePush.Parallel {
		t.Fatal("pre-push.parallel must be false (or omitted): css-verify and lint race on internal/web/static/.brute-tw.src.css (see issue #1146)")
	}
	if !cfg.PrePush.Piped {
		t.Fatal("pre-push.piped must be true so commands run in defined order and stop on first failure (see issue #1146)")
	}
}
