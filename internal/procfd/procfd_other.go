//go:build !darwin

package procfd

func openVnodePaths(int) ([]string, error) {
	return nil, ErrUnsupported
}
