//go:build !darwin

package procfd

import (
	"errors"
	"os"
	"testing"
)

func TestOpenVnodePathsUnsupported(t *testing.T) {
	if _, err := OpenVnodePaths(os.Getpid()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("OpenVnodePaths on non-darwin = %v, want ErrUnsupported", err)
	}
}
