// Package netnstest runs a test in a user and network namespace of its own, where it may add
// interfaces, addresses and routes, and send raw frames, without touching the host's.
package netnstest

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// InNamespace reports whether the test runs in its own user and network namespace. Called
// outside one, it runs the test again in a new one, as root of it, and returns false.
func InNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv("CHALKOS_TEST_NETNS") == "1" {
		return true
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), "CHALKOS_TEST_NETNS=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	out, err := cmd.CombinedOutput()
	t.Logf("in a new network namespace:\n%s", out)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return false
}

// IP runs ip with the arguments, failing the test when it fails.
func IP(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
		t.Fatalf("ip %v: %v\n%s", args, err, out)
	}
}
