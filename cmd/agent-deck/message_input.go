package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// resolveMessageInput merges the inline -m/--message value with --message-file.
// file may be a path or "-" to read stdin. Only one source may be set; returns
// "" when neither is. Trailing newlines are trimmed so the tmux paste does not
// submit a stray empty line after the message.
//
// The file form exists because a long multi-line prompt passed inline through
// a shell gets mangled (backticks, $, quotes) — the documented workaround was
// -m "$(cat task.md)", which still round-trips through shell quoting once.
func resolveMessageInput(inline, file string, stdin io.Reader) (string, error) {
	if file == "" {
		return inline, nil
	}
	if inline != "" {
		return "", fmt.Errorf("use either an inline message or --message-file, not both")
	}
	var data []byte
	var err error
	if file == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(file)
	}
	if err != nil {
		return "", fmt.Errorf("read message file: %w", err)
	}
	msg := strings.TrimRight(string(data), "\r\n")
	if strings.TrimSpace(msg) == "" {
		return "", fmt.Errorf("message file '%s' is empty", file)
	}
	return msg, nil
}
