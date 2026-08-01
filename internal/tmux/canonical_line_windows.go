//go:build windows
// +build windows

package tmux

import "errors"

// ttyLineDiscipline has no meaning on Windows: there is no tty line
// discipline to inspect. Returning an error keeps the caller on the
// "unknown discipline" path — deliver, then verify — rather than
// refusing sends outright.
func ttyLineDiscipline(string) (lineDiscipline, error) {
	return lineDiscipline{}, errors.New("tty line discipline is not inspectable on windows")
}
