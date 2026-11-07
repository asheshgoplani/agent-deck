//go:build !unix

package reportloop

// lockFile has no cross-process lock off unix; Store's mutex still
// serializes changes inside one process.
func lockFile(string) (func() error, error) { return func() error { return nil }, nil }
