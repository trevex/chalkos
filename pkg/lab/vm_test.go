package lab

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/internal/testwait"
)

func hasPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestQemuArgs(t *testing.T) {
	c := VMConfig{
		Name:         "cp1",
		Dir:          "/vm",
		FirmwareCode: "/fw/CODE.fd",
		Disks:        []VMDisk{{Path: "/vm/disk.qcow2"}, {Path: "/vm/data.qcow2", Serial: "chalk-data"}},
		MemoryMB:     2048,
		CPUs:         2,
	}
	args := c.qemuArgs("/vm/tpm/swtpm.sock")

	want := [][2]string{
		{"-drive", "if=pflash,format=raw,unit=0,readonly=on,file=/fw/CODE.fd"},
		{"-drive", "if=pflash,format=raw,unit=1,file=/vm/OVMF_VARS.fd"},
		{"-chardev", "file,id=console,append=on,path=/vm/console.log"},
		{"-serial", "chardev:console"},
		{"-drive", "if=none,id=disk0,format=qcow2,file=/vm/disk.qcow2"},
		{"-device", "virtio-blk-pci,drive=disk0,bootindex=1"},
		{"-drive", "if=none,id=disk1,format=qcow2,file=/vm/data.qcow2"},
		{"-device", "virtio-blk-pci,drive=disk1,bootindex=2,serial=chalk-data"},
		{"-chardev", "socket,id=chrtpm,path=/vm/tpm/swtpm.sock"},
		{"-device", "tpm-tis,tpmdev=tpm0"},
	}
	for _, w := range want {
		if !hasPair(args, w[0], w[1]) {
			t.Errorf("missing %s %s in %v", w[0], w[1], args)
		}
	}

	if !hasPair(args, "-nic", "none") {
		t.Error("a VM without forwards has a NIC")
	}

	c.Forwards = []Forward{{Host: 15000, Guest: 50000}, {Host: 16443, Guest: 6443}, {Host: 15005, Guest: 5000}}
	c.CDROM = "/vm/installer.iso"
	withNIC := c.qemuArgs("")
	for _, w := range [][2]string{
		{"-nic", "user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:15000-:50000,hostfwd=tcp:127.0.0.1:16443-:6443,hostfwd=tcp:127.0.0.1:15005-:5000"},
		{"-drive", "if=none,id=cdrom,media=cdrom,readonly=on,file=/vm/installer.iso"},
		{"-device", "ide-cd,drive=cdrom,bootindex=0"},
	} {
		if !hasPair(withNIC, w[0], w[1]) {
			t.Errorf("missing %s %s in %v", w[0], w[1], withNIC)
		}
	}

	c.GuestForwards = []GuestForward{{Guest: "10.0.2.100:5000", Host: "127.0.0.1:15001"}}
	c.Switch = "/vm/switch"
	c.MAC = "52:54:00:00:01:11"
	switched := c.qemuArgs("")
	for _, w := range [][2]string{
		{"-nic", "user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:15000-:50000,hostfwd=tcp:127.0.0.1:16443-:6443,hostfwd=tcp:127.0.0.1:15005-:5000,guestfwd=tcp:10.0.2.100:5000-cmd:socat - TCP:127.0.0.1:15001"},
		{"-netdev", "vde,id=switch,sock=/vm/switch"},
		{"-device", "virtio-net-pci,netdev=switch,mac=52:54:00:00:01:11"},
	} {
		if !hasPair(switched, w[0], w[1]) {
			t.Errorf("missing %s %s in %v", w[0], w[1], switched)
		}
	}

	if noTPM := c.qemuArgs(""); hasPair(noTPM, "-device", "tpm-tis,tpmdev=tpm0") {
		t.Error("TPM device present without a TPM socket")
	}
	if hasPair(args, "-device", "virtio-serial-pci,id=agent") {
		t.Error("a VM without GuestAgent has its channel")
	}
	c.GuestAgent = true
	agent := c.qemuArgs("")
	for _, w := range [][2]string{
		{"-device", "virtio-serial-pci,id=agent"},
		{"-chardev", "socket,id=qga,path=/vm/qga.sock,server=on,wait=off"},
		{"-device", "virtserialport,bus=agent.0,chardev=qga,name=org.qemu.guest_agent.0"},
	} {
		if !hasPair(agent, w[0], w[1]) {
			t.Errorf("missing %s %s in %v", w[0], w[1], agent)
		}
	}
	c.NoSerialPort = true
	c.Credentials = map[string]string{"b.two": "2", "a.one": "1"}
	bare := c.qemuArgs("")
	if !hasPair(bare, "-serial", "none") || hasPair(bare, "-serial", "chardev:console") {
		t.Errorf("a VM without a serial port has args %q", bare)
	}
	// The console's log stays defined, so the log a test follows exists.
	if !hasPair(bare, "-chardev", "file,id=console,append=on,path="+c.ConsolePath()) {
		t.Errorf("the console's chardev is gone: %q", bare)
	}
	i := slices.Index(bare, "type=11,value=io.systemd.credential:a.one=1")
	j := slices.Index(bare, "type=11,value=io.systemd.credential:b.two=2")
	if i < 1 || j < i || bare[i-1] != "-smbios" || bare[j-1] != "-smbios" {
		t.Errorf("credentials are not SMBIOS strings in name order: %q", bare)
	}
}

func TestKeyChords(t *testing.T) {
	got, err := keyChords("aZ-9\n")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"a"}, {"shift", "z"}, {"minus"}, {"9"}, {"ret"}}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("keyChords = %q, want %q", got, want)
	}
	if _, err := keyChords("é"); err == nil {
		t.Error("a character without a key was typed")
	}
}

// TestStopProcess kills a process that runs with the directory on its command line once the
// timeout passed without it exiting, and leaves a process of another directory alone.
func TestStopProcess(t *testing.T) {
	dir := t.TempDir()
	start := func(arg string) *exec.Cmd {
		t.Helper()
		// The shell waits for input that does not come, with arg on its command line.
		cmd := exec.Command("/bin/sh", "-c", "read line", arg)
		if _, err := cmd.StdinPipe(); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		// The command line reads empty until the new program is set up.
		testwait.Eventually(t, "the shell to run", func() bool { return namesDir(cmd.Process.Pid, arg) })
		return cmd
	}
	other := start(dir + "2")
	defer func() {
		other.Process.Kill()
		other.Wait()
	}()
	stopProcess(other.Process.Pid, dir, 0)
	if !namesDir(other.Process.Pid, dir+"2") {
		t.Error("stopped the process of another directory")
	}
	own := start(dir)
	stopProcess(own.Process.Pid, dir, 0)
	if err := own.Wait(); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Errorf("the process ended with %v, want killed", err)
	}
}
