package session

import (
	"errors"
	"io"
)

// closeFile closes c and joins a close error with any earlier error.
// Deferred on files opened for writing, it reports a failed flush on close.
func closeFile(c io.Closer, errp *error) {
	if cerr := c.Close(); cerr != nil {
		if *errp == nil {
			*errp = cerr
		} else {
			*errp = errors.Join(*errp, cerr)
		}
	}
}
