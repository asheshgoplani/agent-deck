package tmux

import (
	"os/exec"
	"path/filepath"
	"sync/atomic"
)

var subprocessStarts atomic.Int64

// SubprocessStarts counts native tmux commands started through this package.
// Output calls are counted on return; Run and asynchronous calls at Start.
// Deltas across a status pass include concurrent work in the same process.
func SubprocessStarts() int64 { return subprocessStarts.Load() }

func observeCommand(cmd *exec.Cmd) {
	if cmd.Process != nil && filepath.Base(cmd.Path) == "tmux" {
		subprocessStarts.Add(1)
	}
}

func commandRun(cmd *exec.Cmd) error {
	if err := commandStart(cmd); err != nil {
		return err
	}
	return cmd.Wait()
}
func commandStart(cmd *exec.Cmd) error {
	err := cmd.Start()
	if err == nil {
		observeCommand(cmd)
	}
	return err
}
func commandOutput(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Process == nil {
		defer observeCommand(cmd)
	}
	return cmd.Output()
}
func commandCombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	if cmd.Process == nil {
		defer observeCommand(cmd)
	}
	return cmd.CombinedOutput()
}
