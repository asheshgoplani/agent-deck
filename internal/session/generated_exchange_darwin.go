//go:build darwin

package session

import "golang.org/x/sys/unix"

// renamex_np(RENAME_SWAP) is Darwin's atomic pathname-exchange primitive. It
// has the same publication semantics as Linux renameat2(RENAME_EXCHANGE): the
// replacement becomes visible atomically and the displaced destination stays
// available at the temporary path for identity/content validation and rollback.
const generatedFileExchangeSupported = 1

func exchangeGeneratedFile(left, right string) error {
	return unix.RenamexNp(left, right, unix.RENAME_SWAP)
}
