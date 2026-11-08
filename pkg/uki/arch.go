package uki

import (
	"debug/pe"
	"fmt"
	"io"

	"github.com/trevex/chalkos/pkg/arch"
)

// Architecture is the architecture an EFI binary, a UKI or a boot loader, is built for, by its PE
// machine type: x86-64 or arm64.
func Architecture(r io.ReaderAt) (string, error) {
	f, err := pe.NewFile(r)
	if err != nil {
		return "", fmt.Errorf("not an EFI binary: %w", err)
	}
	defer f.Close()
	a, ok := arch.ByPEMachine(f.Machine)
	if !ok {
		return "", fmt.Errorf("built for the machine type %#x, for which chalkos builds no images", f.Machine)
	}
	return a.Name, nil
}
