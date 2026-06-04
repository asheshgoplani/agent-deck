//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// Set the running app to "accessory" so it lives only in the menu bar and
// never gets a Dock icon — equivalent to LSUIElement=true, but applied at
// runtime so it works even when the binary is launched directly (no .app
// bundle). systray creates the shared NSApplication before mbOnReady runs,
// so NSApp is non-nil here.
static void mbSetAccessoryActivationPolicy() {
	[NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
}
*/
import "C"

// mbHideDockIcon removes the Dock icon for this menu-bar-only agent.
func mbHideDockIcon() {
	C.mbSetAccessoryActivationPolicy()
}
