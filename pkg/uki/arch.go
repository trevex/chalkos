package uki

import (
	"debug/pe"
	"fmt"
	"io"
)

// The architectures chalkos builds images for, as systemd and os-release name them, by the PE
// machine type of their EFI binaries.
var architectures = map[uint16]string{
	pe.IMAGE_FILE_MACHINE_AMD64: "x86-64",
	pe.IMAGE_FILE_MACHINE_ARM64: "arm64",
}

// Architecture is the architecture an EFI binary, a UKI or a boot loader, is built for, by its PE
// machine type: x86-64 or arm64.
func Architecture(r io.ReaderAt) (string, error) {
	f, err := pe.NewFile(r)
	if err != nil {
		return "", fmt.Errorf("not an EFI binary: %w", err)
	}
	defer f.Close()
	arch, ok := architectures[f.Machine]
	if !ok {
		return "", fmt.Errorf("built for the machine type %#x, for which chalkos builds no images", f.Machine)
	}
	return arch, nil
}

// GoArchitecture names the architecture of a GOARCH as images name theirs, and "" one chalkos
// builds no images for.
func GoArchitecture(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86-64"
	case "arm64":
		return "arm64"
	}
	return ""
}

// BootLoaderName is the file name in /EFI/BOOT of the boot loader firmware starts from an ESP
// on the architecture, and "" for one chalkos builds no images for.
func BootLoaderName(arch string) string {
	switch arch {
	case "x86-64":
		return "BOOTX64.EFI"
	case "arm64":
		return "BOOTAA64.EFI"
	}
	return ""
}
