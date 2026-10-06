package lab

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// VMConfig describes one QEMU virtual machine with UEFI firmware and an optional software TPM.
type VMConfig struct {
	Name         string
	Dir          string // holds firmware variables, TPM state, sockets, and the console log
	FirmwareCode string // read-only OVMF code image; Secure Boot builds require SMM
	FirmwareVars string // OVMF variable store template, copied into Dir on first start
	Disks        []Disk // attached as virtio-blk devices in boot order
	MemoryMB     int
	CPUs         int
	TPM          bool
}

// Disk is a qcow2 image attached to a VM.
type Disk struct {
	Path string
	// Serial is reported by the virtio-blk device; empty reports none.
	Serial string
}

func (c VMConfig) varsPath() string { return filepath.Join(c.Dir, "OVMF_VARS.fd") }
func (c VMConfig) qmpPath() string  { return filepath.Join(c.Dir, "qmp.sock") }

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
		"-serial", "stdio",
		"-qmp", "unix:" + c.qmpPath() + ",server=on,wait=off",
		"-nic", "none",
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

// VM is a running QEMU process together with its TPM emulator.
type VM struct {
	Config  VMConfig
	Console *Console
	QMP     *QMP

	cmd      *exec.Cmd
	tpm      *SWTPM
	log      *os.File
	exited   chan struct{}
	stopOnce sync.Once
}

// StartVM starts QEMU and connects to its QMP socket. Firmware variables and TPM state already
// present in c.Dir are reused, so stopping and starting a VM behaves like a power cycle.
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
		tpm, err := StartSWTPM(ctx, filepath.Join(c.Dir, "tpm"))
		if err != nil {
			return nil, err
		}
		vm.tpm = tpm
		tpmSocket = tpm.Socket
	}

	log, err := os.OpenFile(filepath.Join(c.Dir, "console.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		vm.cleanup()
		return nil, err
	}
	vm.log = log

	// A pipe we own, rather than cmd.StdoutPipe, so cmd.Wait cannot close it under the console reader.
	r, w, err := os.Pipe()
	if err != nil {
		vm.cleanup()
		return nil, err
	}
	os.Remove(c.qmpPath())
	cmd := exec.Command("qemu-system-x86_64", c.qemuArgs(tpmSocket)...)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		w.Close()
		r.Close()
		vm.cleanup()
		return nil, fmt.Errorf("start qemu: %w", err)
	}
	w.Close()
	vm.cmd = cmd
	vm.Console = NewConsole(r, log)
	go func() {
		cmd.Wait()
		close(vm.exited)
	}()

	qmp, err := DialQMP(ctx, c.qmpPath())
	if err != nil {
		vm.Stop()
		return nil, err
	}
	vm.QMP = qmp
	return vm, nil
}

// Reset is a hard reset, like pressing the machine's reset button. TPM state is kept.
func (vm *VM) Reset() error {
	_, err := vm.QMP.Execute("system_reset", nil)
	return err
}

// Stop terminates QEMU and the TPM emulator. Everything in Config.Dir is kept.
func (vm *VM) Stop() error {
	vm.stopOnce.Do(func() {
		if vm.QMP != nil {
			vm.QMP.Execute("quit", nil)
			vm.QMP.Close()
		}
		if vm.cmd != nil {
			select {
			case <-vm.exited:
			case <-time.After(10 * time.Second):
				vm.cmd.Process.Kill()
				<-vm.exited
			}
		}
		vm.cleanup()
	})
	return nil
}

func (vm *VM) cleanup() {
	if vm.tpm != nil {
		vm.tpm.Stop()
	}
	if vm.log != nil {
		vm.log.Close()
	}
}
