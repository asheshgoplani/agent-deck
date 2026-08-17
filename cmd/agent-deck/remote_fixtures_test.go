package main

import "github.com/asheshgoplani/agent-deck/internal/session"

// Shared fixtures for the --ssh identity tests. Deliberately free of any symbol
// this epic introduces, so the test files that only use pre-existing API still
// compile — and fail — against the unfixed merge base.

// controllerCWD is what `add --ssh` actually stores in ProjectPath: the
// controller's working directory, which has nothing to do with where the
// session runs.
const controllerCWD = "/home/dev/controller-cwd"

func sshInstance(id, title, host, remotePath string) *session.Instance {
	return &session.Instance{
		ID:            id,
		Title:         title,
		ProjectPath:   controllerCWD, // local placeholder, not where it runs
		SSHHost:       host,
		SSHRemotePath: remotePath,
	}
}
