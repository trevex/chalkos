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
		{"-serial", "stdio"},
		{"-qmp", "unix:/vm/qmp.sock,server=on,wait=off"},
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

	if noTPM := c.qemuArgs(""); hasPair(noTPM, "-device", "tpm-tis,tpmdev=tpm0") {
		t.Error("TPM device present without a TPM socket")
	}
}
