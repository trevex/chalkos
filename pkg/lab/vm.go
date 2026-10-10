package lab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// VMConfig describes one QEMU virtual machine with UEFI firmware and an optional software TPM.
type VMConfig struct {
	Name         string
	Dir          string // holds firmware variables, TPM state, sockets, and the console log
	FirmwareCode string // read-only OVMF code image; Secure Boot builds require SMM
	FirmwareVars string // OVMF variable store template, copied into Dir on first start
	// CDROM is an ISO image attached as a CD-ROM drive that boots before the disks.
	CDROM    string
	Disks    []Disk // attached as virtio-blk devices in boot order
	MemoryMB int
	CPUs     int
	TPM      bool
	// Forwards adds a user-mode NIC whose host ports on 127.0.0.1 reach guest ports; the guest
	// gets its address from QEMU's DHCP server. Without forwards the VM has no NIC.
	Forwards []Forward
	// GuestForwards connect addresses the guest reaches through the user-mode NIC to host
	// addresses; socat carries each connection. They add the user-mode NIC too.
	GuestForwards []GuestForward
	// Switch attaches a second NIC with the address MAC to the vde switch whose sockets are in
	// this directory, for traffic between VMs.
	Switch string
	MAC    string
	// GuestAgent adds the channel the QEMU guest agent answers on, at GuestAgentSocket.
	GuestAgent bool
}

// GuestForward makes a host address reachable at a guest address, such as 10.0.2.100:5000.
type GuestForward struct {
	Guest string `json:"guest"`
	Host  string `json:"host"`
}

// Forward makes a guest TCP port reachable on a host port.
type Forward struct {
	Host, Guest int
}

// Disk is a qcow2 image attached to a VM.
type Disk struct {
	Path string
	// Serial is reported by the virtio-blk device; empty reports none.
	Serial string
}

func (c VMConfig) varsPath() string { return filepath.Join(c.Dir, "OVMF_VARS.fd") }
func (c VMConfig) qmpPath() string  { return filepath.Join(c.Dir, "qmp.sock") }
func (c VMConfig) pidPath() string  { return filepath.Join(c.Dir, "qemu.pid") }
func (c VMConfig) tpmDir() string   { return filepath.Join(c.Dir, "tpm") }

// ConsolePath is the file QEMU appends the VM's serial console to, across restarts.
func (c VMConfig) ConsolePath() string { return filepath.Join(c.Dir, "console.log") }

// GuestAgentSocket is where the host reaches the QEMU guest agent of a VM with GuestAgent.
func (c VMConfig) GuestAgentSocket() string { return filepath.Join(c.Dir, "qga.sock") }

func (c VMConfig) qemuArgs(tpmSocket string) []string {
	args := []string{
		"-name", c.Name,
		"-machine", "q35,smm=on,accel=kvm:tcg",
		"-cpu", "max",
		"-m", strconv.Itoa(c.MemoryMB),
		"-smp", strconv.Itoa(c.CPUs),
		"-global", "driver=cfi.pflash01,property=secure,value=on",
		"-drive", "if=pflash,format=raw,unit=0,readonly=on,file=" + c.FirmwareCode,
		"-drive", "if=pflash,format=raw,unit=1,file=" + c.varsPath(),
		"-display", "none",
		"-monitor", "none",
		// QEMU writes the console itself, so it outlives whoever started the VM.
		"-chardev", "file,id=console,append=on,path=" + c.ConsolePath(),
		"-serial", "chardev:console",
		"-qmp", "unix:" + c.qmpPath() + ",server=on,wait=off",
		"-pidfile", c.pidPath(),
	}
	if len(c.Forwards)+len(c.GuestForwards) == 0 {
		args = append(args, "-nic", "none")
	} else {
		nic := "user,model=virtio-net-pci"
		for _, f := range c.Forwards {
			nic += fmt.Sprintf(",hostfwd=tcp:127.0.0.1:%d-:%d", f.Host, f.Guest)
		}
		for _, f := range c.GuestForwards {
			nic += fmt.Sprintf(",guestfwd=tcp:%s-cmd:socat - TCP:%s", f.Guest, f.Host)
		}
		args = append(args, "-nic", nic)
	}
	if c.Switch != "" {
		args = append(args,
			"-netdev", "vde,id=switch,sock="+c.Switch,
			"-device", "virtio-net-pci,netdev=switch,mac="+c.MAC)
	}
	if c.GuestAgent {
		args = append(args,
			"-device", "virtio-serial-pci,id=agent",
			"-chardev", "socket,id=qga,path="+c.GuestAgentSocket()+",server=on,wait=off",
			"-device", "virtserialport,bus=agent.0,chardev=qga,name=org.qemu.guest_agent.0")
	}
	if c.CDROM != "" {
		args = append(args,
			"-drive", "if=none,id=cdrom,media=cdrom,readonly=on,file="+c.CDROM,
			"-device", "ide-cd,drive=cdrom,bootindex=0")
	}
	for i, disk := range c.Disks {
		id := fmt.Sprintf("disk%d", i)
		device := fmt.Sprintf("virtio-blk-pci,drive=%s,bootindex=%d", id, i+1)
		if disk.Serial != "" {
			device += ",serial=" + disk.Serial
		}
		args = append(args,
			"-drive", fmt.Sprintf("if=none,id=%s,format=qcow2,file=%s", id, disk.Path),
			"-device", device)
	}
	if tpmSocket != "" {
		args = append(args,
			"-chardev", "socket,id=chrtpm,path="+tpmSocket,
			"-tpmdev", "emulator,id=tpm0,chardev=chrtpm",
			"-device", "tpm-tis,tpmdev=tpm0")
	}
	return args
}

// VM is a running QEMU process together with its TPM emulator: one this process started, or one
// it attached to.
type VM struct {
	Config  VMConfig
	Console *Console
	QMP     *QMP

	// cmd and tpm are set on a VM this process started; pid on one it attached to.
	cmd      *exec.Cmd
	tpm      *SWTPM
	pid      int
	log      *os.File
	follower *follower
	exited   chan struct{}
	stopOnce sync.Once
}

// StartVM starts QEMU and connects to its QMP socket. Firmware variables and TPM state already
// present in c.Dir are reused, so stopping and starting a VM behaves like a power cycle. The
// console shows only what the VM writes from now on.
func StartVM(ctx context.Context, c VMConfig) (*VM, error) {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(c.varsPath()); errors.Is(err, fs.ErrNotExist) {
		if err := copyFile(c.FirmwareVars, c.varsPath(), 0o644); err != nil {
			return nil, fmt.Errorf("copy firmware variables: %w", err)
		}
	}

	vm := &VM{Config: c, exited: make(chan struct{})}
	tpmSocket := ""
	if c.TPM {
		tpm, err := StartSWTPM(ctx, c.tpmDir())
		if err != nil {
			return nil, err
		}
		vm.tpm = tpm
		tpmSocket = tpm.Socket
	}

	console, err := os.OpenFile(c.ConsolePath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		vm.cleanup()
		return nil, err
	}
	start, err := console.Seek(0, 2)
	console.Close()
	if err != nil {
		vm.cleanup()
		return nil, err
	}
	if vm.Console, vm.follower, err = followConsole(c.ConsolePath(), start); err != nil {
		vm.cleanup()
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(c.Dir, "qemu.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		vm.cleanup()
		return nil, err
	}
	vm.log = log

	os.Remove(c.qmpPath())
	cmd := exec.Command("qemu-system-x86_64", c.qemuArgs(tpmSocket)...)
	cmd.Stdout = log
	cmd.Stderr = log
	if err := cmd.Start(); err != nil {
		vm.cleanup()
		return nil, fmt.Errorf("start qemu: %w", err)
	}
	vm.cmd = cmd
	go func() {
		cmd.Wait()
		close(vm.exited)
	}()

	qmpCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-vm.exited:
			cancel()
		case <-qmpCtx.Done():
		}
	}()
	qmp, err := DialQMP(qmpCtx, c.qmpPath())
	if err != nil {
		vm.Stop()
		if out, _ := os.ReadFile(log.Name()); len(out) > 0 {
			return nil, fmt.Errorf("%w; qemu: %s", err, out)
		}
		return nil, err
	}
	vm.QMP = qmp
	return vm, nil
}

// AttachVM reaches a VM that runs already, as another process started it: its QMP socket and its
// console, read from the start of the log.
func AttachVM(ctx context.Context, c VMConfig) (*VM, error) {
	pid, ok := runningPID(c.pidPath(), c.Dir)
	if !ok {
		return nil, fmt.Errorf("the VM %s does not run", c.Name)
	}
	vm := &VM{Config: c, pid: pid}
	var err error
	if vm.Console, vm.follower, err = followConsole(c.ConsolePath(), 0); err != nil {
		return nil, err
	}
	if vm.QMP, err = DialQMP(ctx, c.qmpPath()); err != nil {
		vm.follower.Close()
		return nil, err
	}
	return vm, nil
}

// Reset is a hard reset, like pressing the machine's reset button. TPM state is kept.
func (vm *VM) Reset() error {
	_, err := vm.QMP.Execute("system_reset", nil)
	return err
}

// SetLink connects or disconnects the VM's NIC on the switch, like pulling its cable.
func (vm *VM) SetLink(up bool) error {
	_, err := vm.QMP.Execute("set_link", map[string]any{"name": "switch", "up": up})
	return err
}

// Stop terminates QEMU and the TPM emulator. Everything in Config.Dir is kept.
func (vm *VM) Stop() error {
	vm.stopOnce.Do(func() {
		switch {
		case vm.QMP != nil:
			vm.QMP.Execute("quit", nil)
			vm.QMP.Close()
		case vm.cmd != nil:
			// QEMU quits on SIGTERM too, as without a QMP connection.
			vm.cmd.Process.Signal(syscall.SIGTERM)
		case vm.pid != 0:
			syscall.Kill(vm.pid, syscall.SIGTERM)
		}
		switch {
		case vm.cmd != nil:
			select {
			case <-vm.exited:
			case <-time.After(10 * time.Second):
				vm.cmd.Process.Kill()
				<-vm.exited
			}
		case vm.pid != 0:
			stopProcess(vm.pid, vm.Config.Dir, 10*time.Second)
			stopSWTPMIn(vm.Config.tpmDir())
		}
		vm.cleanup()
	})
	return nil
}

// Detach lets go of the VM without stopping it: QEMU and its TPM emulator keep running, and
// AttachVM reaches them again.
func (vm *VM) Detach() {
	vm.stopOnce.Do(func() {
		if vm.QMP != nil {
			vm.QMP.Close()
		}
		if vm.follower != nil {
			vm.follower.Close()
		}
		if vm.log != nil {
			vm.log.Close()
		}
	})
}

func (vm *VM) cleanup() {
	if vm.tpm != nil {
		vm.tpm.Stop()
	}
	if vm.follower != nil {
		vm.follower.Close()
	}
	if vm.log != nil {
		vm.log.Close()
	}
}

// runningPID reads the PID a process wrote to a file and reports whether it runs still: a
// process whose command line names dir, so a PID used again by another process is not taken for
// it.
func runningPID(pidFile, dir string) (int, bool) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(string(bytes.TrimSpace(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	if !namesDir(pid, dir) {
		return 0, false
	}
	return pid, true
}

// namesDir reports whether the process runs with dir, or a path in it, among its arguments, so
// a directory whose name begins the same, such as a lab of another cluster, does not count.
func namesDir(pid int, dir string) bool {
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	for _, arg := range bytes.Split(cmdline, []byte{0}) {
		if string(arg) == dir || bytes.Contains(arg, []byte(dir+"/")) {
			return true
		}
	}
	return false
}

// stopProcess ends the process that runs with dir on its command line: it waits for it to exit
// on its own until timeout, then kills it.
func stopProcess(pid int, dir string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for namesDir(pid, dir) {
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			deadline = time.Now().Add(timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// FreePort returns a TCP port on 127.0.0.1 that is free now, for a forward.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
