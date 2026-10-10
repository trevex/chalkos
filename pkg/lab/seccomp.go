package lab

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// DenyIOURing makes io_uring unavailable to this process and to the programs it starts from now
// on, such as QEMU: QEMU's io_uring main loop loses TPM emulator commands
// (https://gitlab.com/qemu-project/qemu/-/issues/4581), and without io_uring it falls back to
// epoll. io_uring_setup fails with ENOSYS, as on a kernel without io_uring. The seccomp filter
// that does so needs no_new_privs, which it sets: the process and its children gain no
// privileges by running setuid programs.
func DenyIOURing() error {
	if runtime.GOARCH != "amd64" {
		return errors.New("a lab runs on x86-64 alone")
	}
	filter := []unix.SockFilter{
		// x86-64's io_uring_setup alone is denied. 32-bit compat calls report another architecture,
		// whose numbers differ, and pass unfiltered; x32 calls report x86-64 with the x32 bit set in
		// their number, so they pass too. QEMU, a 64-bit program, makes neither.
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 3, K: unix.AUDIT_ARCH_X86_64},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: unix.SYS_IO_URING_SETUP},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	// no_new_privs is a thread's, and seccomp needs it on the thread that installs the filter.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	// Every thread of the process, so whichever thread starts a program has the filter.
	thread, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_TSYNC, uintptr(unsafe.Pointer(&prog)))
	if errno != 0 {
		return fmt.Errorf("deny io_uring: %w", errno)
	}
	if thread != 0 {
		return fmt.Errorf("deny io_uring: thread %d cannot take the filter", thread)
	}
	runtime.KeepAlive(filter)
	return nil
}
