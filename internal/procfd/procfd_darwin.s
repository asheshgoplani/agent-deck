//go:build darwin

#include "textflag.h"

// libSystem trampolines for the libproc calls in procfd_darwin.go, in the
// same form golang.org/x/sys/unix generates for every darwin libc function.

TEXT libc_proc_pidinfo_trampoline<>(SB), NOSPLIT, $0-0
	JMP	libc_proc_pidinfo(SB)

GLOBL	·libc_proc_pidinfo_trampoline_addr(SB), RODATA, $8
DATA	·libc_proc_pidinfo_trampoline_addr(SB)/8, $libc_proc_pidinfo_trampoline<>(SB)

TEXT libc_proc_pidfdinfo_trampoline<>(SB), NOSPLIT, $0-0
	JMP	libc_proc_pidfdinfo(SB)

GLOBL	·libc_proc_pidfdinfo_trampoline_addr(SB), RODATA, $8
DATA	·libc_proc_pidfdinfo_trampoline_addr(SB)/8, $libc_proc_pidfdinfo_trampoline<>(SB)
