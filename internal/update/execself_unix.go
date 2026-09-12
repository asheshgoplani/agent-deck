//go:build !windows

package update

import "syscall"

// execSelf replaces the running process image. On success it never returns.
func execSelf(exe string, args, env []string) error {
	// #nosec G702 -- exe is the path os.Executable() resolved at startup
	// (or now), args are this process's own os.Args and env its own
	// environment; nothing here comes from user or network input.
	return syscall.Exec(exe, args, env)
}
