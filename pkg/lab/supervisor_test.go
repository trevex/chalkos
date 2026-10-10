package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// testLab writes the state of a lab of two nodes to a new state directory.
func testLab(t *testing.T) (string, *Lab) {
	t.Helper()
	fakeTools(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir, err := StateDir("lab")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	vars := filepath.Join(dir, "OVMF_VARS.fd")
	if err := os.WriteFile(vars, []byte("vars"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &Lab{Cluster: "lab", FirmwareCode: "/fw/CODE.fd", FirmwareVars: vars, Nodes: []LabNode{
		{Name: "cp1", Role: "controlplane", Kind: "controlplane", MAC: "52:54:00:7b:00:11", MemoryMB: 3072, CPUs: 2, ChalkdPort: 15001, APIPort: 16443},
		{Name: "w1", Role: "worker", Kind: "worker", MAC: "52:54:00:7b:00:12", MemoryMB: 2048, CPUs: 2, ChalkdPort: 15002},
	}}
	if err := l.Write(dir); err != nil {
		t.Fatal(err)
	}
	if err := l.CheckSockets(dir); err != nil {
		t.Fatal(err)
	}
	// Whatever of the lab a test leaves running, as when it fails halfway, stops once the
	// supervisors the test runs did.
	t.Cleanup(func() {
		// StopLab would send this process the SIGTERM a supervisor stops on.
		if pid, running, _ := Supervisor(dir); running && pid == os.Getpid() {
			t.Error("the test's supervisor runs on")
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := StopLab(ctx, dir); err != nil {
			t.Errorf("stop the lab: %v", err)
		}
	})
	return dir, l
}

// syncBuffer is a buffer the supervisor logs to while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// supervise runs the lab's supervisor, logging to log, until the test ends or the returned stop
// is called, and waits until it marked the lab ready.
func supervise(t *testing.T, dir string, log io.Writer) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Supervise(ctx, dir, log) }()
	stop = sync.OnceValue(func() error {
		cancel()
		return <-done
	})
	t.Cleanup(func() { stop() })
	deadline := time.Now().Add(30 * time.Second)
	for !Ready(dir) {
		select {
		case err := <-done:
			t.Fatalf("the supervisor ended before the lab was ready: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("the lab did not get ready")
		}
	}
	return stop
}

// TestSupervisorRunsTheLab starts the lab's VMs with their TPMs and the switch, records its PID,
// refuses a second supervisor, lets others attach to the VMs, detach and attach again, and stops
// everything when it ends.
func TestSupervisorRunsTheLab(t *testing.T) {
	dir, l := testLab(t)
	stop := supervise(t, dir, io.Discard)

	if pid, running, err := Supervisor(dir); err != nil || !running || pid != os.Getpid() {
		t.Errorf("Supervisor = %d, %v, %v; want this process", pid, running, err)
	}
	if err := Supervise(context.Background(), dir, io.Discard); err == nil || !strings.Contains(err.Error(), "a supervisor runs the lab") {
		t.Errorf("a second supervisor = %v", err)
	}
	for _, n := range l.Nodes {
		c := l.VMConfig(dir, n)
		if !Running(c) {
			t.Errorf("%s does not run", n.Name)
		}
		vars, err := os.ReadFile(filepath.Join(c.Dir, "OVMF_VARS.fd"))
		if err != nil || string(vars) != "vars" {
			t.Errorf("%s's firmware variables = %q, %v; want the lab's", n.Name, vars, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cp1 := l.VMConfig(dir, l.Nodes[0])
	for attach := range 2 {
		vm, err := AttachVM(ctx, cp1)
		if err != nil {
			t.Fatalf("attach %d: %v", attach, err)
		}
		m, err := vm.Console.WaitFor(ctx, regexp.MustCompile(`certificate fingerprint ([0-9a-f]{64})`))
		if err != nil || m[1] != fakeFingerprint {
			t.Errorf("attach %d: the console = %v, %v; want the fingerprint", attach, m, err)
		}
		if err := vm.SetLink(false); err != nil {
			t.Errorf("attach %d: QMP: %v", attach, err)
		}
		// QEMU serves one QMP client at a time.
		busy, cancelBusy := context.WithTimeout(ctx, 200*time.Millisecond)
		if _, err := AttachVM(busy, cp1); err == nil || !strings.Contains(err.Error(), "another client may hold the QMP socket") {
			t.Errorf("attach %d: a second client = %v", attach, err)
		}
		cancelBusy()
		vm.Detach()
		if !Running(cp1) {
			t.Fatalf("attach %d: detaching stopped the VM", attach)
		}
	}

	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, running, _ := Supervisor(dir); running {
		t.Error("the supervisor still runs")
	}
	if Ready(dir) {
		t.Error("the lab is ready without a supervisor")
	}
	for _, n := range l.Nodes {
		c := l.VMConfig(dir, n)
		if Running(c) {
			t.Errorf("%s runs on", n.Name)
		}
		if _, ok := runningPID(filepath.Join(c.tpmDir(), "swtpm.pid"), c.tpmDir()); ok {
			t.Errorf("the TPM of %s runs on", n.Name)
		}
	}
	if _, ok := runningPID(filepath.Join(dir, "switch.pid"), filepath.Join(dir, "switch")); ok {
		t.Error("the switch runs on")
	}
	if _, err := AttachVM(ctx, cp1); err == nil || !strings.Contains(err.Error(), "does not run") {
		t.Errorf("attach to a stopped VM = %v", err)
	}
}

// TestStopLabWithoutSupervisor stops VMs, TPMs and a switch that run on without the supervisor
// that started them, as after it was killed.
func TestStopLabWithoutSupervisor(t *testing.T) {
	dir, l := testLab(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sw, err := StartSwitch(ctx, filepath.Join(dir, "switch"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sw.Stop)
	for _, n := range l.Nodes {
		vm, err := StartVM(ctx, l.VMConfig(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		vm.Detach()
	}
	if err := StopLab(ctx, dir); err != nil {
		t.Fatal(err)
	}
	for _, n := range l.Nodes {
		c := l.VMConfig(dir, n)
		if Running(c) {
			t.Errorf("%s runs on", n.Name)
		}
		if _, ok := runningPID(filepath.Join(c.tpmDir(), "swtpm.pid"), c.tpmDir()); ok {
			t.Errorf("the TPM of %s runs on", n.Name)
		}
	}
	if _, ok := runningPID(filepath.Join(dir, "switch.pid"), filepath.Join(dir, "switch")); ok {
		t.Error("the switch runs on")
	}
}

// TestStopLabStopsTheSupervisor asks a running supervisor to stop the lab.
func TestStopLabStopsTheSupervisor(t *testing.T) {
	dir, l := testLab(t)
	stop := supervise(t, dir, io.Discard)
	// The supervisor is this process, which takes the SIGTERM StopLab sends as chalklab does.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	defer signal.Stop(sig)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-sig
		stop()
	}()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()
	if err := StopLab(stopCtx, dir); err != nil {
		t.Fatal(err)
	}
	<-stopped
	if Running(l.VMConfig(dir, l.Nodes[1])) {
		t.Error("w1 runs on")
	}
}

// TestStateDir keeps labs in $XDG_STATE_HOME/chalklab, or ~/.local/state/chalklab.
func TestStateDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if dir, err := StateDir("lab"); err != nil || dir != "/state/chalklab/lab" {
		t.Errorf("StateDir = %q, %v", dir, err)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/alice")
	if dir, err := StateDir("lab"); err != nil || dir != "/home/alice/.local/state/chalklab/lab" {
		t.Errorf("StateDir without XDG_STATE_HOME = %q, %v", dir, err)
	}
	t.Setenv("XDG_STATE_HOME", "state")
	if _, err := StateDir("lab"); err == nil {
		t.Error("took a relative XDG_STATE_HOME")
	}
	l := &Lab{Nodes: []LabNode{{Name: "cp1"}}}
	if err := l.CheckSockets("/" + strings.Repeat("d", 100)); err == nil || !strings.Contains(err.Error(), "XDG_STATE_HOME") {
		t.Errorf("CheckSockets of a long directory = %v", err)
	}
}

// TestLabVMConfig forwards chalkd and a control plane's API server and attaches the lab's disk,
// switch, TPM and guest agent.
func TestLabVMConfig(t *testing.T) {
	l := &Lab{FirmwareCode: "/fw/CODE.fd", FirmwareVars: "/lab/OVMF_VARS.fd", GuestForwards: []GuestForward{{Guest: "10.0.2.100:5000", Host: "127.0.0.1:5000"}}}
	c := l.VMConfig("/lab", LabNode{Name: "cp1", MAC: "52:54:00:7b:00:11", MemoryMB: 3072, CPUs: 2, ChalkdPort: 15001, APIPort: 16443})
	data, _ := json.Marshal(c)
	want := VMConfig{Name: "cp1", Dir: "/lab/cp1", FirmwareCode: "/fw/CODE.fd", FirmwareVars: "/lab/OVMF_VARS.fd",
		Disks: []Disk{{Path: "/lab/cp1/disk.qcow2"}}, MemoryMB: 3072, CPUs: 2, TPM: true,
		Forwards: []Forward{{Host: 15001, Guest: 50000}, {Host: 16443, Guest: 6443}}, GuestForwards: l.GuestForwards,
		Switch: "/lab/switch", MAC: "52:54:00:7b:00:11", GuestAgent: true}
	wantData, _ := json.Marshal(want)
	if string(data) != string(wantData) {
		t.Errorf("VMConfig = %s, want %s", data, wantData)
	}
}

// TestPingGuestAgent passes over answers to earlier requests until guest-sync's own, then pings.
func TestPingGuestAgent(t *testing.T) {
	dir, err := os.MkdirTemp("", "qga")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	vm := &VM{Config: VMConfig{Dir: dir}}
	l, err := net.Listen("unix", vm.Config.GuestAgentSocket())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var mu sync.Mutex
	var commands []string
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		dec := json.NewDecoder(conn)
		for {
			var req struct {
				Execute   string
				Arguments struct{ ID int64 }
			}
			if dec.Decode(&req) != nil {
				return
			}
			mu.Lock()
			commands = append(commands, req.Execute)
			mu.Unlock()
			switch req.Execute {
			case "guest-sync":
				fmt.Fprintf(conn, "{\"return\": 12}\n{\"return\": %d}\n", req.Arguments.ID)
			case "guest-ping":
				fmt.Fprintln(conn, `{"return": {}}`)
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := vm.PingGuestAgent(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(commands, " ") != "guest-sync guest-ping" {
		t.Errorf("commands %q", commands)
	}
}

// TestVMExitKeepsTheLab keeps the lab's other VMs running when one exits, as when its guest
// powers off, and starts it again when asked, with its ports.
func TestVMExitKeepsTheLab(t *testing.T) {
	dir, l := testLab(t)
	var log syncBuffer
	supervise(t, dir, &log)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cp1, w1 := l.VMConfig(dir, l.Nodes[0]), l.VMConfig(dir, l.Nodes[1])
	cp1PID, _ := runningPID(cp1.pidPath(), cp1.Dir)
	pid, ok := runningPID(w1.pidPath(), w1.Dir)
	if !ok {
		t.Fatal("w1 does not run")
	}
	syscall.Kill(pid, syscall.SIGTERM)
	for Ready(dir) || Running(w1) {
		select {
		case <-ctx.Done():
			t.Fatal("the lab is ready after w1 exited")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if _, running, err := Supervisor(dir); err != nil || !running {
		t.Fatalf("the supervisor ended after w1 exited: %v", err)
	}
	if pid, ok := runningPID(cp1.pidPath(), cp1.Dir); !ok || pid != cp1PID {
		t.Error("cp1 does not run on after w1 exited")
	}
	if !strings.Contains(log.String(), "the VM of w1 exited") {
		t.Errorf("the supervisor logged %q, want w1's exit", log.String())
	}
	if _, ok := runningPID(filepath.Join(w1.tpmDir(), "swtpm.pid"), w1.tpmDir()); ok {
		t.Error("the TPM of w1 runs on after its VM exited")
	}

	if err := StartStopped(ctx, dir); err != nil {
		t.Fatal(err)
	}
	if !Ready(dir) {
		t.Error("the lab is not ready once w1 started again")
	}
	pid, ok = runningPID(w1.pidPath(), w1.Dir)
	if !ok {
		t.Fatal("w1 does not run after the start")
	}
	cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if want := fmt.Sprintf("hostfwd=tcp:127.0.0.1:%d-:50000", l.Nodes[1].ChalkdPort); !strings.Contains(string(cmdline), want) {
		t.Errorf("w1 runs without its port: %q", cmdline)
	}
	if pid, ok := runningPID(cp1.pidPath(), cp1.Dir); !ok || pid != cp1PID {
		t.Error("the start restarted cp1, which ran")
	}
	// A lab whose VMs all run has nothing to start.
	if err := StartStopped(ctx, dir); err != nil {
		t.Errorf("StartStopped of a running lab = %v", err)
	}
}

// TestStartAgain refuses to start a lab a supervisor runs, and starts a lab no supervisor runs
// again from its state, over the stale sockets a host's reboot leaves.
func TestStartAgain(t *testing.T) {
	dir, l := testLab(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := supervise(t, dir, io.Discard)
	if err := PrepareStart(ctx, dir); err == nil || !strings.Contains(err.Error(), "runs the lab") {
		t.Errorf("PrepareStart of a supervised lab = %v, want a refusal", err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if err := StartStopped(ctx, dir); err == nil || !strings.Contains(err.Error(), "no supervisor") {
		t.Errorf("StartStopped without a supervisor = %v", err)
	}

	// A host's reboot leaves the switch's socket behind.
	os.Remove(filepath.Join(dir, "switch", "ctl"))
	if err := os.MkdirAll(filepath.Join(dir, "switch"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "switch", "ctl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareStart(ctx, dir); err != nil {
		t.Fatal(err)
	}
	supervise(t, dir, io.Discard)
	for _, n := range l.Nodes {
		c := l.VMConfig(dir, n)
		pid, ok := runningPID(c.pidPath(), c.Dir)
		if !ok {
			t.Fatalf("%s does not run after the start", n.Name)
		}
		cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if want := fmt.Sprintf("hostfwd=tcp:127.0.0.1:%d-:50000", n.ChalkdPort); !strings.Contains(string(cmdline), want) {
			t.Errorf("%s runs without its port: %q", n.Name, cmdline)
		}
	}
}

// TestStartSwitchOverAStaleSocket waits for the new switch, not the socket a switch that is gone
// left behind.
func TestStartSwitchOverAStaleSocket(t *testing.T) {
	fakeTools(t)
	dir := filepath.Join(t.TempDir(), "switch")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ctl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := StartSwitch(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	conn, err := net.Dial("unix", filepath.Join(dir, "ctl"))
	if err != nil {
		t.Fatalf("the switch does not listen once started: %v", err)
	}
	conn.Close()
}

// TestSupervisorPIDNotYetWritten waits for the PID of a supervisor that holds the lock but has
// not written its PID yet.
func TestSupervisorPIDNotYetWritten(t *testing.T) {
	dir := t.TempDir()
	lock, err := os.OpenFile(filepath.Join(dir, supervisorFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		lock.WriteString("4242\n")
	}()
	if pid, running, err := Supervisor(dir); err != nil || !running || pid != 4242 {
		t.Errorf("Supervisor = %d, %v, %v; want 4242 once written", pid, running, err)
	}
}

// TestRunningPIDMatchesTheDirectory takes a process for one of the lab's only when its command
// line names the lab's directory, not another whose name begins the same.
func TestRunningPIDMatchesTheDirectory(t *testing.T) {
	fakeTools(t)
	base := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	other := filepath.Join(base, "lab2", "switch")
	s, err := StartSwitch(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if _, ok := runningPID(other+".pid", other); !ok {
		t.Error("the switch is not taken for its own directory's")
	}
	if _, ok := runningPID(other+".pid", filepath.Join(base, "lab")); ok {
		t.Error("the switch of lab2 is taken for one of lab")
	}
	if _, ok := runningPID(other+".pid", filepath.Join(base, "lab2")); !ok {
		t.Error("the switch of lab2 is not taken for one of lab2")
	}
}

// fakeGuestAgent answers guest-sync as the agent does and reports guest-shutdown with the answer
// given, closing the connection without one, as the agent's guest goes away.
func fakeGuestAgent(t *testing.T, vm *VM, shutdownError string) <-chan string {
	t.Helper()
	l, err := net.Listen("unix", vm.Config.GuestAgentSocket())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	commands := make(chan string, 10)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		dec := json.NewDecoder(conn)
		for {
			var req struct {
				Execute   string
				Arguments struct{ ID int64 }
			}
			if dec.Decode(&req) != nil {
				return
			}
			commands <- req.Execute
			switch {
			case req.Execute == "guest-sync":
				fmt.Fprintf(conn, "{\"return\": %d}\n", req.Arguments.ID)
			case req.Execute == "guest-shutdown" && shutdownError != "":
				fmt.Fprintf(conn, "{\"error\": {\"class\": \"GenericError\", \"desc\": %q}}\n", shutdownError)
			case req.Execute == "guest-shutdown":
				return
			}
		}
	}()
	return commands
}

// TestShutdownGuest asks the guest agent to shut the guest down, which answers only when it
// cannot.
func TestShutdownGuest(t *testing.T) {
	for _, failure := range []string{"", "Failed to execute child process “/sbin/poweroff”"} {
		dir, err := os.MkdirTemp("", "qga")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		vm := &VM{Config: VMConfig{Dir: dir}}
		commands := fakeGuestAgent(t, vm, failure)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = vm.ShutdownGuest(ctx)
		if failure == "" && err != nil || failure != "" && (err == nil || !strings.Contains(err.Error(), failure)) {
			t.Errorf("ShutdownGuest with the agent answering %q = %v", failure, err)
		}
		if got := []string{<-commands, <-commands}; strings.Join(got, " ") != "guest-sync guest-shutdown" {
			t.Errorf("commands %q", got)
		}
	}
}
