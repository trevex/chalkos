package lab

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ioURingSetup sets up an io_uring, as QEMU's main loop does, and prints the errno it fails with.
func ioURingSetup() {
	var params [120]byte
	fd, _, errno := unix.Syscall(unix.SYS_IO_URING_SETUP, 4, uintptr(unsafe.Pointer(&params)), 0)
	if errno != 0 {
		fmt.Println(errno.Error())
		os.Exit(0)
	}
	unix.Close(int(fd))
	fmt.Println("set up")
	os.Exit(0)
}

// TestDenyIOURing makes io_uring unavailable to the programs a process starts afterwards, so QEMU
// falls back to its epoll main loop.
func TestDenyIOURing(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, name := range []string{"deny-io-uring", "io-uring-setup"} {
		if err := os.Symlink(self, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(filepath.Join(bin, "deny-io-uring"), filepath.Join(bin, "io-uring-setup")).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != unix.ENOSYS.Error() {
		t.Errorf("io_uring_setup after DenyIOURing: %q, want %q", got, unix.ENOSYS.Error())
	}
}
