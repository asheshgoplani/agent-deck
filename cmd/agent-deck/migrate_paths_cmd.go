package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

func handleMigratePaths(args []string) {
	if code := runMigratePaths(args, os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func runMigratePaths(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migrate-paths", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "Show what would be copied without writing files")
	force := fs.Bool("force", false, "Overwrite existing XDG destination files")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: agent-deck migrate-paths [--dry-run] [--force]")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Copy legacy ~/.agent-deck files into the XDG config/data/cache layout.")
		fmt.Fprintln(stderr, "The legacy directory is left untouched.")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Options:")
		fmt.Fprintln(stderr, "  --dry-run  Show what would be copied without writing files")
		fmt.Fprintln(stderr, "  --force    Overwrite existing XDG destination files")
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected argument: %s\n", fs.Arg(0))
		fs.Usage()
		return 2
	}

	legacyDir, err := agentpaths.LegacyDir()
	if err != nil {
		fmt.Fprintf(stderr, "failed to resolve legacy directory: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, "Migrating legacy ~/.agent-deck paths to XDG layout")
	result, err := agentpaths.MigrateLegacyLayout(agentpaths.MigrationOptions{
		DryRun: *dryRun,
		Force:  *force,
	})
	if result != nil {
		printMigrationResult(stdout, result)
	}
	if err != nil {
		if errors.Is(err, agentpaths.ErrMigrationConflict) {
			fmt.Fprintln(stderr, "rerun with --force to overwrite destination files")
		} else {
			fmt.Fprintf(stderr, "migration failed: %v\n", err)
		}
		return 1
	}
	if result == nil || (len(result.Copied) == 0 && len(result.Skipped) == 0) {
		fmt.Fprintln(stdout, "nothing to migrate")
	}
	fmt.Fprintf(stdout, "legacy directory left untouched: %s\n", legacyDir)
	return 0
}

func printMigrationResult(stdout io.Writer, result *agentpaths.MigrationResult) {
	action := "copied"
	if result.DryRun {
		action = "would copy"
	}
	for _, item := range result.Copied {
		fmt.Fprintf(stdout, "%s %s %s -> %s\n", action, item.Category, item.Name, item.Destination)
	}
	for _, item := range result.Skipped {
		fmt.Fprintf(stdout, "skipped %s %s\n", item.Category, item.Name)
	}
	for _, item := range result.Conflicts {
		fmt.Fprintf(stdout, "conflict %s %s already exists at %s\n", item.Category, item.Name, item.Destination)
	}
}
