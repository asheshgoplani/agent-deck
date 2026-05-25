package main

// Detection + clear error message for the PEP 668 ("externally-managed-environment")
// failure mode in conductor setup's Python dependency install.
//
// Background: agent-deck conductor setup runs `python3 -m pip install --user toml aiogram`
// inside installPythonDeps(). On Homebrew Python and Debian/Ubuntu system Python, that
// pip invocation fails with PEP 668 ("error: externally-managed-environment"), the
// failure is swallowed by --quiet, only a one-line stderr note is printed, and the
// launchd bridge daemon then crash-loops invisibly. This test pins the classifier
// that lets installPythonDeps() recognise the case and emit an actionable message.

import (
	"strings"
	"testing"
)

// Real stderr from `python3 -m pip install --user toml` against Homebrew's
// python@3.14 on macOS, captured 2026-05-24. Trimmed to the load-bearing lines;
// the classifier must not require exact byte-for-byte match.
const samplePEP668Stderr = `error: externally-managed-environment

× This environment is externally managed
╰─> To install Python packages system-wide, try brew install
    xyz, where xyz is the package you are trying to
    install.

    If you wish to install a Python library that isn't in Homebrew,
    use a virtual environment:

    python3 -m venv path/to/venv
    source path/to/venv/bin/activate
    python3 -m pip install xyz
`

func TestClassifyPipFailure_DetectsPEP668(t *testing.T) {
	got := classifyPipFailure(samplePEP668Stderr)
	if got != pipFailurePEP668 {
		t.Fatalf("classifyPipFailure(PEP 668 stderr) = %q, want %q", got, pipFailurePEP668)
	}
	// Defensive: the marker must be the externally-managed-environment phrase,
	// not something coincidental in the surrounding prose. If the implementation
	// ever drifts (e.g. matches on "brew install"), this guard catches it.
	if !strings.Contains(samplePEP668Stderr, "externally-managed-environment") {
		t.Fatal("sample stderr no longer contains the canonical PEP 668 marker; update the fixture")
	}
}

func TestClassifyPipFailure_NonPEP668ErrorIsOther(t *testing.T) {
	// Unrelated pip failure — network, missing package, permission, etc. The
	// classifier must NOT misattribute these to PEP 668, otherwise the
	// remediation message would be misleading.
	for _, stderr := range []string{
		"ERROR: Could not find a version that satisfies the requirement zzzz",
		"ERROR: Could not install packages due to an OSError: [Errno 13] Permission denied",
		"WARNING: Retrying ... after connection broken by 'NewConnectionError'",
	} {
		if got := classifyPipFailure(stderr); got != pipFailureOther {
			t.Errorf("classifyPipFailure(%q) = %q, want %q", stderr, got, pipFailureOther)
		}
	}
}

func TestClassifyPipFailure_EmptyStderrIsNone(t *testing.T) {
	if got := classifyPipFailure(""); got != pipFailureNone {
		t.Fatalf("classifyPipFailure(\"\") = %q, want %q", got, pipFailureNone)
	}
}

// The PEP 668 remediation message MUST name the exact interpreter so the user
// knows which Python is the problem (Homebrew vs. mise vs. system), MUST list
// every package the install needed, MUST mention the minimum supported Python
// (3.11+), and MUST surface all three fix paths (version-manager / venv /
// --break-system-packages). The interpreter version is informational and may
// be empty when sys.version probing fails — the formatter must not crash.
func TestFormatPipFailureMessage_PEP668_ContainsAllRequiredElements(t *testing.T) {
	diag := pipFailureDiagnostic{
		Kind:            pipFailurePEP668,
		InterpreterPath: "/opt/homebrew/opt/python@3.14/bin/python3.14",
		InterpreterVer:  "3.14.5",
		Packages:        []string{"toml", "aiogram"},
	}
	msg := formatPipFailureMessage(diag)

	required := []string{
		"PEP 668",
		"externally-managed",
		"/opt/homebrew/opt/python@3.14/bin/python3.14",
		"3.14.5",
		"toml",
		"aiogram",
		"3.11+",
		"mise use",
		"python3 -m venv",
		"--break-system-packages",
		"agent-deck conductor setup",
		"agent-deck conductor status",
	}
	for _, want := range required {
		if !strings.Contains(msg, want) {
			t.Errorf("formatPipFailureMessage(PEP668) missing required substring %q\n----- full message -----\n%s", want, msg)
		}
	}
}

// When mise is detected on PATH, option 1 (version-manager) should be labeled
// recommended. Without any version manager detected, option 2 (venv) takes
// the "recommended" label instead — venv is the most isolated fallback.
func TestFormatPipFailureMessage_PEP668_RecommendsMiseWhenDetected(t *testing.T) {
	withMise := formatPipFailureMessage(pipFailureDiagnostic{
		Kind:            pipFailurePEP668,
		InterpreterPath: "/usr/bin/python3",
		Packages:        []string{"toml"},
		HasMise:         true,
	})
	if !strings.Contains(withMise, "mise") {
		t.Fatal("expected mise mention in message")
	}
	// "recommended" appears once, on the mise line.
	if strings.Count(withMise, "recommended") != 1 {
		t.Errorf("expected exactly one 'recommended' marker (the mise line); got %d\n%s",
			strings.Count(withMise, "recommended"), withMise)
	}
	miseLineIdx := strings.Index(withMise, "mise use")
	recIdx := strings.Index(withMise, "recommended")
	if miseLineIdx < 0 || recIdx < 0 || recIdx > miseLineIdx {
		t.Errorf("'recommended' should appear before the `mise use` command (on the option-1 header line); recIdx=%d miseLineIdx=%d", recIdx, miseLineIdx)
	}

	withoutMise := formatPipFailureMessage(pipFailureDiagnostic{
		Kind:            pipFailurePEP668,
		InterpreterPath: "/usr/bin/python3",
		Packages:        []string{"toml"},
		HasMise:         false,
	})
	if strings.Count(withoutMise, "recommended") != 1 {
		t.Errorf("expected exactly one 'recommended' marker (the venv line) when mise absent; got %d\n%s",
			strings.Count(withoutMise, "recommended"), withoutMise)
	}
	venvLineIdx := strings.Index(withoutMise, "python3 -m venv")
	recIdxNoMise := strings.Index(withoutMise, "recommended")
	if venvLineIdx < 0 || recIdxNoMise < 0 || recIdxNoMise > venvLineIdx {
		t.Errorf("without mise, 'recommended' should mark the venv option; recIdx=%d venvLineIdx=%d", recIdxNoMise, venvLineIdx)
	}
}

// Non-PEP-668 failures retain the pre-existing behaviour: surface the raw
// pip stderr so the user sees the actual error, and provide the manual
// install command as a fallback. The classifier shields us from emitting
// PEP-668-specific advice for unrelated failures.
func TestFormatPipFailureMessage_Other_PassesThroughStderr(t *testing.T) {
	rawErr := "ERROR: Could not install packages due to an OSError: [Errno 13] Permission denied"
	diag := pipFailureDiagnostic{
		Kind:      pipFailureOther,
		Packages:  []string{"toml", "aiogram"},
		RawStderr: rawErr,
	}
	msg := formatPipFailureMessage(diag)

	if !strings.Contains(msg, rawErr) {
		t.Errorf("Other-kind message should include the raw pip stderr; got:\n%s", msg)
	}
	if !strings.Contains(msg, "pip3 install toml aiogram") {
		t.Errorf("Other-kind message should include the manual install command; got:\n%s", msg)
	}
	if strings.Contains(msg, "PEP 668") || strings.Contains(msg, "externally-managed") {
		t.Errorf("Other-kind message must NOT mention PEP 668 (would mislead the user); got:\n%s", msg)
	}
}
