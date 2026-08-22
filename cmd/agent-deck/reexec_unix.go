//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

var startupExecutablePath, startupExecutableErr = resolveExecutableNow()

func resolveExecutableNow() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func resolvedExecutable() (string, error) {
	return startupExecutablePath, startupExecutableErr
}

func reexecSelf(version string) error {
	exe, err := resolvedExecutable()
	if err != nil {
		return err
	}
	env := append(os.Environ(), "AGENTDECK_UPDATED="+version)
	return syscall.Exec(exe, os.Args, env)
}
