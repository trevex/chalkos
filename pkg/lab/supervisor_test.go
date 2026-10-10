package lab

import (
	"context"
	"encoding/json"
	"fmt"
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
	return dir, l
}

// supervise runs the lab's supervisor until the test ends or the returned stop is called, and
// waits until it marked the lab ready.
func supervise(t *testing.T, dir string) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Supervise(ctx, dir) }()
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
	stop := supervise(t, dir)

	if pid, running, err := Supervisor(dir); err != nil || !running || pid != os.Getpid() {
		t.Errorf("Supervisor = %d, %v, %v; want this process", pid, running, err)
	}
	if err := Supervise(context.Background(), dir); err == nil || !strings.Contains(err.Error(), "a supervisor runs the lab") {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Supervise(ctx, dir) }()
	for !Ready(dir) {
		time.Sleep(20 * time.Millisecond)
	}
	// The supervisor is this process, which takes the SIGTERM StopLab sends as chalklab does.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	defer signal.Stop(sig)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-sig
		cancel()
		<-done
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
