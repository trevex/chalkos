package lab

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The test binary stands in for QEMU, swtpm and vde_switch when it runs under their names, as
// fakeTools links them: each writes its PID file and serves its sockets the way the tool does, and
// fake QEMU writes a maintenance chalkd's console lines.
func TestMain(m *testing.M) {
	switch filepath.Base(os.Args[0]) {
	case "qemu-system-x86_64":
		exitWithTest()
		fakeQEMU(os.Args[1:])
	case "swtpm":
		exitWithTest()
		fakeSWTPM(os.Args[1:])
	case "vde_switch":
		exitWithTest()
		fakeSwitch(os.Args[1:])
	case "deny-io-uring":
		// Deny io_uring, then start a program as chalklab starts QEMU.
		if err := DenyIOURing(); err != nil {
			fakeFail(err)
		}
		if err := syscall.Exec(os.Args[1], os.Args[1:], os.Environ()); err != nil {
			fakeFail(err)
		}
	case "io-uring-setup":
		ioURingSetup()
	default:
		os.Exit(m.Run())
	}
}

// exitWithTest ends a fake tool once the test binary that started it is gone, as when go test is
// interrupted or killed: no cleanup of the test stops the tool then.
func exitWithTest() {
	parent := os.Getppid()
	go func() {
		for os.Getppid() == parent {
			time.Sleep(200 * time.Millisecond)
		}
		os.Exit(1)
	}()
}

// fakeFingerprint is what fake QEMU's chalkd prints as its certificate's fingerprint.
var fakeFingerprint = strings.Repeat("ab", 32)

// fakeTools puts the fake QEMU, swtpm and vde_switch first on the PATH, and fails the test if one
// of them outlives it.
func fakeTools(t *testing.T) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := noProcessLeft(t)
	for _, name := range []string{"qemu-system-x86_64", "swtpm", "vde_switch"} {
		if err := os.Symlink(self, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// flagValue returns the argument after flag.
func flagValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

// option returns the value of key in an option list such as type=unixio,path=/x.
func option(list, key string) string {
	for _, kv := range strings.Split(list, ",") {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

func fakeFail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

// listenUnix listens on a Unix socket and exits on SIGTERM, as the tools do.
func listenUnix(path string) net.Listener {
	os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		fakeFail(err)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	go func() {
		<-stop
		os.Exit(0)
	}()
	return l
}

func fakeQEMU(args []string) {
	var console string
	for i, a := range args {
		if a == "-chardev" && i+1 < len(args) && strings.HasPrefix(args[i+1], "file,id=console") {
			console = option(args[i+1], "path")
		}
	}
	f, err := os.OpenFile(console, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fakeFail(err)
	}
	if err := os.WriteFile(flagValue(args, "-pidfile"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
		fakeFail(err)
	}
	qmp := listenUnix(strings.TrimSuffix(strings.TrimPrefix(flagValue(args, "-qmp"), "unix:"), ",server=on,wait=off"))
	fmt.Fprintf(f, "booting %s\r\nchalkd: maintenance mode, accepting clients of the OS CA; certificate fingerprint %s\n", flagValue(args, "-name"), fakeFingerprint)
	f.Close()
	for {
		conn, err := qmp.Accept()
		if err != nil {
			fakeFail(err)
		}
		// As QEMU, one client at a time: others wait for the greeting.
		func() {
			defer conn.Close()
			fmt.Fprintln(conn, `{"QMP": {"version": {}, "capabilities": []}}`)
			sc := bufio.NewScanner(conn)
			for sc.Scan() {
				var req struct{ Execute string }
				json.Unmarshal(sc.Bytes(), &req)
				fmt.Fprintln(conn, `{"return": {}}`)
				if req.Execute == "quit" {
					os.Exit(0)
				}
			}
		}()
	}
}

func fakeSWTPM(args []string) {
	l := listenUnix(option(flagValue(args, "--ctrl"), "path"))
	if err := os.WriteFile(option(flagValue(args, "--pid"), "file"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
		fakeFail(err)
	}
	for {
		if _, err := l.Accept(); err != nil {
			fakeFail(err)
		}
	}
}

func fakeSwitch(args []string) {
	dir := flagValue(args, "--sock")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fakeFail(err)
	}
	l := listenUnix(filepath.Join(dir, "ctl"))
	if err := os.WriteFile(flagValue(args, "--pidfile"), []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644); err != nil {
		fakeFail(err)
	}
	for {
		if _, err := l.Accept(); err != nil {
			fakeFail(err)
		}
	}
}

// noProcessLeft returns a new temporary directory of the test and fails the test if a process
// that names one of its temporary directories on its command line runs when the test ends, after
// the cleanups registered later stopped what they started. It kills such a process, so no VM,
// TPM or switch of a test runs on after it.
func noProcessLeft(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Dir(dir)
	t.Cleanup(func() {
		procs, _ := os.ReadDir("/proc")
		for _, p := range procs {
			pid, err := strconv.Atoi(p.Name())
			if err != nil || pid == os.Getpid() || !namesDir(pid, root) {
				continue
			}
			cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
			syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("process %d runs on after the test: %s", pid, strings.ReplaceAll(strings.TrimRight(string(cmdline), "\x00"), "\x00", " "))
		}
	})
	return dir
}
