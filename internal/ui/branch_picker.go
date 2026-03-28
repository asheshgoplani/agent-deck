package ui

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

var openBranchPicker = branchPickerCmd

type branchPickerResultMsg struct {
	branch   string
	canceled bool
	err      error
}

func branchPickerCmd(projectPath string) tea.Cmd {
	selected := ""
	canceled := false

	cmd := &branchPickerExecCmd{
		projectPath: projectPath,
		selected:    &selected,
		canceled:    &canceled,
	}

	return tea.Exec(cmd, func(err error) tea.Msg {
		return branchPickerResultMsg{
			branch:   selected,
			canceled: canceled,
			err:      err,
		}
	})
}

type branchPickerExecCmd struct {
	projectPath string
	selected    *string
	canceled    *bool
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
}

func (c *branchPickerExecCmd) Run() error {
	if _, err := exec.LookPath("fzf"); err != nil {
		return errors.New("fzf not found; install fzf or type branch manually")
	}

	projectPath := session.ExpandPath(strings.Trim(strings.TrimSpace(c.projectPath), "'\""))
	if projectPath == "" {
		return errors.New("project path is empty")
	}

	repoRoot, err := git.GetWorktreeBaseRoot(projectPath)
	if err != nil {
		return errors.New("path is not a git repository")
	}

	branches, err := git.ListBranchCandidates(repoRoot)
	if err != nil {
		return err
	}
	if len(branches) == 0 {
		return errors.New("no branches found in repository")
	}

	var output bytes.Buffer
	fzf := exec.Command("fzf", "--prompt", "Branch> ", "--height", "40%", "--reverse")
	fzf.Stdin = strings.NewReader(strings.Join(branches, "\n") + "\n")
	fzf.Stdout = &output
	if c.stderr != nil {
		fzf.Stderr = c.stderr
	} else {
		fzf.Stderr = os.Stderr
	}

	if err := fzf.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 1, 130:
				if c.canceled != nil {
					*c.canceled = true
				}
				return nil
			}
		}
		return fmt.Errorf("fzf failed: %w", err)
	}

	selected := strings.TrimSpace(output.String())
	if selected == "" {
		if c.canceled != nil {
			*c.canceled = true
		}
		return nil
	}
	if c.selected != nil {
		*c.selected = selected
	}
	return nil
}

func (c *branchPickerExecCmd) SetStdin(r io.Reader)  { c.stdin = r }
func (c *branchPickerExecCmd) SetStdout(w io.Writer) { c.stdout = w }
func (c *branchPickerExecCmd) SetStderr(w io.Writer) { c.stderr = w }
