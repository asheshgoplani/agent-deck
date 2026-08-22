//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
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
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = environmentForUpdate(os.Environ(), version)
	if err := cmd.Start(); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
