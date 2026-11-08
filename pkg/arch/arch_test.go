package arch

import (
	"debug/pe"
	"testing"
)

// TestLookups finds each architecture by every name and number it goes by, and none for those of
// architectures chalkos builds no images for.
func TestLookups(t *testing.T) {
	for _, a := range All {
		for name, got := range map[string]func() (Arch, bool){
			"ByName":      func() (Arch, bool) { return ByName(a.Name) },
			"ByGOARCH":    func() (Arch, bool) { return ByGOARCH(a.GOARCH) },
			"ByPEMachine": func() (Arch, bool) { return ByPEMachine(a.PEMachine) },
		} {
			if found, ok := got(); !ok || found != a {
				t.Errorf("%s of %s = %+v, %v", name, a.Name, found, ok)
			}
		}
		for typ, wantVerity := range map[string]bool{a.Usr: false, a.UsrVerity: true} {
			if found, verity, ok := ByStoreType(typ); !ok || found != a || verity != wantVerity {
				t.Errorf("ByStoreType(%s) = %s, %v, %v", typ, found.Name, verity, ok)
			}
		}
		for typ, wantVerity := range map[string]bool{"usr-" + a.Name: false, "usr-" + a.Name + "-verity": true} {
			if found, verity, ok := ByRepartType(typ); !ok || found != a || verity != wantVerity {
				t.Errorf("ByRepartType(%s) = %s, %v, %v", typ, found.Name, verity, ok)
			}
		}
	}
	if _, ok := ByName("riscv64"); ok {
		t.Error("ByName found riscv64")
	}
	if _, ok := ByGOARCH("386"); ok {
		t.Error("ByGOARCH found 386")
	}
	if _, ok := ByPEMachine(pe.IMAGE_FILE_MACHINE_I386); ok {
		t.Error("ByPEMachine found i386")
	}
	for _, typ := range []string{"usr-riscv64", "usr-riscv64-verity", "root-x86-64", "esp", "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"} {
		if _, _, ok := ByRepartType(typ); ok {
			t.Errorf("ByRepartType found %s", typ)
		}
		if _, _, ok := ByStoreType(typ); ok {
			t.Errorf("ByStoreType found %s", typ)
		}
	}
}
