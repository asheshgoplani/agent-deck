//go:build darwin

package voicestate

import "syscall"

func statCtimeNs(st *syscall.Stat_t) int64 { return st.Ctimespec.Nano() }
