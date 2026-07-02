package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/artifact"
)

// handleArtifact dispatches `agent-deck artifact <subcommand>`. The only MVP
// subcommand is `stamp`, which writes the provenance sidecar the Fleet Console
// routes highlight-to-comment annotations on. See internal/artifact.
func handleArtifact(profile string, args []string) {
	if err := runArtifact(os.Stdout, profile, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runArtifact is the testable seam.
func runArtifact(stdout io.Writer, profile string, args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(stdout, "Usage: agent-deck artifact stamp <file.html> [flags]")
		return fmt.Errorf("expected a subcommand (stamp)")
	}
	switch args[0] {
	case "stamp":
		return runArtifactStamp(stdout, profile, args[1:])
	default:
		return fmt.Errorf("unknown artifact subcommand %q", args[0])
	}
}

// runArtifactStamp writes "<file>.meta.json" next to an artifact HTML so the
// Fleet Console can attribute it to its owning session. Explicit flags win;
// the artifact id and group derive from the conductor/<name>/<file>.html layout
// when omitted, and the session falls back to AGENTDECK_INSTANCE_ID so a
// session can stamp its own output with a flagless call.
func runArtifactStamp(stdout io.Writer, profile string, args []string) error {
	fs := flag.NewFlagSet("artifact stamp", flag.ContinueOnError)
	var (
		sessionID  = fs.String("session", "", "owning session id (defaults to $AGENTDECK_INSTANCE_ID)")
		artifactID = fs.String("id", "", "artifact id (defaults to the filename without .html)")
		group      = fs.String("group", "", "owning group/conductor (defaults to the parent directory name)")
		title      = fs.String("title", "", "human title (defaults to the artifact id)")
		profileARG = fs.String("profile", "", "owning profile (defaults to -p)")
	)
	fs.Usage = func() {
		fmt.Fprintln(stdout, "Usage: agent-deck artifact stamp <file.html> [--session id] [--id id] [--group g] [--title t] [--profile p]")
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Writes <file>.meta.json provenance so the Fleet Console can route")
		fmt.Fprintln(stdout, "highlight-to-comment annotations back to the owning session.")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("expected exactly one html file argument")
	}
	htmlPath := fs.Arg(0)
	if _, err := os.Stat(htmlPath); err != nil {
		return fmt.Errorf("artifact file not readable: %w", err)
	}

	base := strings.TrimSuffix(filepath.Base(htmlPath), ".html")
	id := firstNonEmpty(*artifactID, base)
	grp := firstNonEmpty(*group, filepath.Base(filepath.Dir(htmlPath)))
	sess := firstNonEmpty(*sessionID, os.Getenv("AGENTDECK_INSTANCE_ID"))
	prof := firstNonEmpty(*profileARG, profile)

	meta := artifact.Meta{
		ArtifactID: id,
		SessionID:  sess,
		Group:      grp,
		Profile:    prof,
		Title:      firstNonEmpty(*title, id),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := artifact.WriteMeta(htmlPath, meta); err != nil {
		return fmt.Errorf("write sidecar: %w", err)
	}
	fmt.Fprintf(stdout, "stamped %s (session=%s group=%s)\n", artifact.SidecarPath(htmlPath), meta.SessionID, meta.Group)
	return nil
}
