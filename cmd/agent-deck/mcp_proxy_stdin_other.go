//go:build !darwin && !linux

package main

func mcpProxyStdinClosed() bool {
	return false
}
