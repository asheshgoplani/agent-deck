package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

func runWorktreeCheckpoint(args []string, out, errOut io.Writer) int {
	usage := func() {
		fmt.Fprintln(out, "Usage: agent-deck worktree checkpoint save --output FILE [--note FILE]")
		fmt.Fprintln(out, "       agent-deck worktree checkpoint preview FILE")
		fmt.Fprintln(out, "       agent-deck worktree checkpoint restore FILE --path NEW_PATH [--expect SHA256]")
	}
	if len(args) == 0 || helpRequested(args[:1]) || args[0] == "help" {
		usage()
		return 0
	}
	signals, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signals, 2*time.Minute)
	defer cancel()
	err := worktreeCheckpoint(ctx, args, out, errOut)
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return 0
	}
	fmt.Fprintf(errOut, "Error: checkpoint: %v\n", err)
	return 1
}

func worktreeCheckpoint(ctx context.Context, args []string, out, errOut io.Writer) error {
	fs := flag.NewFlagSet("worktree checkpoint "+args[0], flag.ContinueOnError)
	fs.SetOutput(errOut)
	switch args[0] {
	case "save":
		output := fs.String("output", "", "Create a checkpoint at this path")
		noteFile := fs.String("note", "", "Plain text handoff file, up to 16 KiB")
		if err := fs.Parse(normalizeArgs(fs, args[1:])); err != nil {
			return err
		}
		if *output == "" || fs.NArg() != 0 {
			return errors.New("save requires --output FILE and no positional arguments")
		}
		note, err := git.ReadTaskCheckpointNote(*noteFile)
		if err != nil {
			return err
		}
		c, err := git.CaptureTaskCheckpoint(ctx, ".", note)
		if err != nil {
			return err
		}
		if err := git.WriteTaskCheckpoint(*output, c); err != nil {
			return err
		}
		fmt.Fprintf(out, "Saved checkpoint %s at base %s\n", strconv.Quote(*output), c.Base)
	case "preview", "restore":
		var target, expect string
		if args[0] == "restore" {
			fs.StringVar(&target, "path", "", "New worktree path, outside the current checkout")
			fs.StringVar(&expect, "expect", "", "Require the SHA256 printed by preview")
		}
		if err := fs.Parse(normalizeArgs(fs, args[1:])); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("expected one checkpoint file")
		}
		if args[0] == "restore" && target == "" {
			return errors.New("restore requires --path NEW_PATH")
		}
		c, checksum, err := git.ReadTaskCheckpoint(fs.Arg(0))
		if err != nil {
			return err
		}
		if args[0] == "restore" {
			if expect != "" && expect != checksum {
				return errors.New("checkpoint checksum does not match preview")
			}
			if err := git.RestoreTaskCheckpoint(ctx, ".", target, c); err != nil {
				return err
			}
			fmt.Fprintf(out, "Restored checkpoint %s to %s\n", checksum, strconv.Quote(target))
			return nil
		}
		paths, err := c.Paths(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Base: %s\nSHA256: %s\nTracked paths: %d\n", c.Base, checksum, len(paths))
		for _, name := range paths {
			fmt.Fprintln(out, strconv.QuoteToASCII(name))
		}
		if c.Note != "" {
			fmt.Fprintf(out, "Handoff: %s\n", strconv.QuoteToASCII(c.Note))
		}
	default:
		return fmt.Errorf("unknown checkpoint command %q", args[0])
	}
	return nil
}
