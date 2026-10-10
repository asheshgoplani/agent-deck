package session

import "golang.org/x/sys/unix"

func publishImageUpload(dirFD int, temp, name string) error {
	return unix.Renameat2(dirFD, temp, dirFD, name, unix.RENAME_NOREPLACE)
}
