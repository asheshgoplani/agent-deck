//go:build windows

package update

import "os"

// Windows has no flock; installs there are not serialized by this lock.
func flockExclusiveNB(f *os.File) (bool, error) { return true, nil }

func flockRelease(f *os.File) {}
