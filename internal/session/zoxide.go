package session

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

// ZoxideAvailable reports whether the zoxide binary is reachable on PATH.
func ZoxideAvailable() bool {
	_, err := exec.LookPath("zoxide")
	return err == nil
}

// ZoxideQuery returns matching directory paths from zoxide's database, ordered
// by frecency. An empty query returns the full list. Returns an empty slice
// (not an error) when zoxide has no matches for the query.
func ZoxideQuery(ctx context.Context, query string) ([]string, error) {
	args := []string{"query", "--list"}
	if q := strings.TrimSpace(query); q != "" {
		args = append(args, q)
	}
	cmd := exec.CommandContext(ctx, "zoxide", args...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	return parseZoxideOutput(out), nil
}

func parseZoxideOutput(out []byte) []string {
	var paths []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths
}
