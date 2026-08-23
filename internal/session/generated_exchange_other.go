//go:build !linux && !darwin

package session

import "fmt"

const generatedFileExchangeSupported = 0

func exchangeGeneratedFile(_, _ string) error {
	return fmt.Errorf("atomic generated-file migration is unsupported on this platform")
}
