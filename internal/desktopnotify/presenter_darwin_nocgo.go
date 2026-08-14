//go:build darwin && !cgo

package desktopnotify

import "fmt"

func NativePresentationAvailable() bool { return false }

func NativePresent(Event) error { return fmt.Errorf("desktop notifications require cgo on macOS") }
