package session

import "golang.org/x/sys/unix"

func publishImageUpload(dirFD int, temp, name string) error {
	return unix.RenameatxNp(dirFD, temp, dirFD, name, unix.RENAME_EXCL)
}
