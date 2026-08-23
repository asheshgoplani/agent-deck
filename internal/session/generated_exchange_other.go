//go:build !linux

package session

import "fmt"

func exchangeGeneratedFile(_, _ string) error {
	return fmt.Errorf("atomic generated-file migration is unsupported on this platform")
}
