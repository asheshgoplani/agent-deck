package session

import "context"

// SetSSHRunnerRunFnForTest assigns the unexported runFn field on an SSHRunner.
// Tests in other packages (notably internal/ui) need this to stub out SSH
// command execution without spawning a real ssh subprocess.
//
// Do not call from non-test code; runFn is meant for test injection only.
func SetSSHRunnerRunFnForTest(r *SSHRunner, fn func(args ...string) ([]byte, error)) {
	r.runFn = func(_ context.Context, args ...string) ([]byte, error) {
		return fn(args...)
	}
}
