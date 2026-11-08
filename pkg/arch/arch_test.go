package arch

import (
	"debug/pe"
	"encoding/json"
	"maps"
	"os"
	"strings"
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

// TestGPTTypesFile checks that gpt-types.json, which the installer's Nix module reads, names the
// ESP and the store partition types of every architecture in All, as repart names them, with the
// table's GUIDs, and nothing else.
func TestGPTTypesFile(t *testing.T) {
	data, err := os.ReadFile("gpt-types.json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"esp": "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"}
	for _, a := range All {
		want["usr-"+a.Name] = a.Usr
		want["usr-"+a.Name+"-verity"] = a.UsrVerity
	}
	for name, guid := range got {
		got[name] = strings.ToLower(guid)
	}
	if !maps.Equal(got, want) {
		t.Errorf("gpt-types.json = %v, want %v", got, want)
	}
}
