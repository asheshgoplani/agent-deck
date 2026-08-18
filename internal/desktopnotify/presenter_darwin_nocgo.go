//go:build darwin && !cgo

package desktopnotify

import "fmt"

func NativePresentationAvailable() bool { return false }

func NativeServe(serve func() error) error { return serve() }

func NativePresent(Event) error { return fmt.Errorf("desktop notifications require cgo on macOS") }
