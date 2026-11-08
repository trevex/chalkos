// Package arch lists the architectures chalkos builds images for, with every name and number each
// goes by. An architecture is added by adding its row to All.
package arch

import (
	"debug/pe"
	"runtime"
	"strings"
)

// Arch is an architecture chalkos builds images for.
type Arch struct {
	// Name is the architecture as systemd, os-release and an image's header name it, such as
	// x86-64. repart names the store's partition types after it: usr-<Name> and
	// usr-<Name>-verity.
	Name string
	// GOARCH is the architecture as Go names it.
	GOARCH string
	// PEMachine is the PE machine type of the architecture's EFI binaries, a UKI or a boot loader.
	PEMachine uint16
	// BootLoader is the file name in /EFI/BOOT of the boot loader firmware starts from an ESP.
	BootLoader string
	// Usr and UsrVerity are the GPT partition types of a store slot's data and hash tree
	// partitions.
	Usr, UsrVerity string
}

// All are the architectures chalkos builds images for.
var All = []Arch{
	{
		Name: "x86-64", GOARCH: "amd64", PEMachine: pe.IMAGE_FILE_MACHINE_AMD64, BootLoader: "BOOTX64.EFI",
		Usr: "8484680c-9521-48c6-9c11-b0720656f69e", UsrVerity: "77ff5f63-e7b6-4633-acf4-1565b864c0e6",
	},
	{
		Name: "arm64", GOARCH: "arm64", PEMachine: pe.IMAGE_FILE_MACHINE_ARM64, BootLoader: "BOOTAA64.EFI",
		Usr: "b0e01050-ee5f-4390-949a-9101b17104e9", UsrVerity: "6e11a4e7-fbca-4ded-b9e9-e1a512bb664e",
	},
}

func find(match func(Arch) bool) (Arch, bool) {
	for _, a := range All {
		if match(a) {
			return a, true
		}
	}
	return Arch{}, false
}

// ByName finds the architecture of a name such as x86-64.
func ByName(name string) (Arch, bool) {
	return find(func(a Arch) bool { return a.Name == name })
}

// ByGOARCH finds the architecture of a GOARCH such as amd64.
func ByGOARCH(goarch string) (Arch, bool) {
	return find(func(a Arch) bool { return a.GOARCH == goarch })
}

// ByPEMachine finds the architecture of an EFI binary's PE machine type.
func ByPEMachine(machine uint16) (Arch, bool) {
	return find(func(a Arch) bool { return a.PEMachine == machine })
}

// ByStoreType finds the architecture of a store slot's GPT partition type, in lower case, and
// reports whether it is the type of the hash tree partition.
func ByStoreType(typ string) (a Arch, verity, ok bool) {
	a, ok = find(func(a Arch) bool { return typ == a.Usr || typ == a.UsrVerity })
	return a, ok && typ == a.UsrVerity, ok
}

// ByRepartType finds the architecture of a store partition type as repart names it,
// usr-<Name> or usr-<Name>-verity, and reports whether it is the type of the hash tree partition.
func ByRepartType(typ string) (a Arch, verity, ok bool) {
	name, ok := strings.CutPrefix(typ, "usr-")
	if !ok {
		return Arch{}, false, false
	}
	name, verity = strings.CutSuffix(name, "-verity")
	a, ok = ByName(name)
	return a, verity && ok, ok
}

// Native is the architecture the running program is built for, and the zero Arch when chalkos
// builds no images for it.
func Native() Arch {
	a, _ := ByGOARCH(runtime.GOARCH)
	return a
}
