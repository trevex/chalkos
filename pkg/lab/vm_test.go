package lab

import "testing"

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
		Disks:        []Disk{{Path: "/vm/disk.qcow2"}, {Path: "/vm/data.qcow2", Serial: "chalk-data"}},
		MemoryMB:     2048,
		CPUs:         2,
	}
	args := c.qemuArgs("/vm/tpm/swtpm.sock")

	want := [][2]string{
		{"-machine", "q35,smm=on,accel=kvm:tcg"},
		{"-global", "driver=cfi.pflash01,property=secure,value=on"},
		{"-drive", "if=pflash,format=raw,unit=0,readonly=on,file=/fw/CODE.fd"},
		{"-drive", "if=pflash,format=raw,unit=1,file=/vm/OVMF_VARS.fd"},
		{"-chardev", "file,id=console,append=on,path=/vm/console.log"},
		{"-serial", "chardev:console"},
		{"-qmp", "unix:/vm/qmp.sock,server=on,wait=off"},
		{"-pidfile", "/vm/qemu.pid"},
		{"-drive", "if=none,id=disk0,format=qcow2,file=/vm/disk.qcow2"},
		{"-device", "virtio-blk-pci,drive=disk0,bootindex=1"},
		{"-drive", "if=none,id=disk1,format=qcow2,file=/vm/data.qcow2"},
		{"-device", "virtio-blk-pci,drive=disk1,bootindex=2,serial=chalk-data"},
		{"-chardev", "socket,id=chrtpm,path=/vm/tpm/swtpm.sock"},
		{"-device", "tpm-tis,tpmdev=tpm0"},
		{"-m", "2048"},
	}
	for _, w := range want {
		if !hasPair(args, w[0], w[1]) {
			t.Errorf("missing %s %s in %v", w[0], w[1], args)
		}
	}

	if !hasPair(args, "-nic", "none") {
		t.Error("a VM without forwards has a NIC")
	}

	c.Forwards = []Forward{{Host: 15000, Guest: 50000}, {Host: 16443, Guest: 6443}}
	c.CDROM = "/vm/installer.iso"
	withNIC := c.qemuArgs("")
	for _, w := range [][2]string{
		{"-nic", "user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:15000-:50000,hostfwd=tcp:127.0.0.1:16443-:6443"},
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
		{"-nic", "user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:15000-:50000,hostfwd=tcp:127.0.0.1:16443-:6443,guestfwd=tcp:10.0.2.100:5000-cmd:socat - TCP:127.0.0.1:15001"},
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
}

func TestFreePort(t *testing.T) {
	port, err := FreePort()
	if err != nil || port <= 0 {
		t.Fatalf("FreePort() = %d, %v", port, err)
	}
}
