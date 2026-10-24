package uki

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

const rootHash = "a334de1f08f79f925c9c65187238eb212b5f408c378f0f3a361ee8d395c94ddd"

func TestRead(t *testing.T) {
	img := ukitest.UKI(map[string]string{
		"IMAGE_ID":           "chalkos",
		"IMAGE_VERSION":      "",
		"CHALKOS_VARIANT":    "worker",
		"CHALKOS_PLATFORM":   "metal",
		"CHALKOS_BOOT_TRIES": "3",
		"PRETTY_NAME":        `NixOS "26.05"`,
	}, "init=/nix/store/x/init console=ttyS0 usrhash="+rootHash+"\n")
	got, err := Read(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := got.OSRelease["IMAGE_VERSION"]; got.ID() != "chalkos" || !ok || v != "" || got.Variant() != "worker" || got.BootTries() != "3" {
		t.Errorf("os-release %v", got.OSRelease)
	}
	if got.OSRelease["PRETTY_NAME"] != `NixOS "26.05"` {
		t.Errorf("PRETTY_NAME = %q", got.OSRelease["PRETTY_NAME"])
	}
	if h, err := got.UsrHash(); err != nil || hex.EncodeToString(h) != rootHash {
		t.Errorf("UsrHash = %x, %v", h, err)
	}

	if _, err := Read(bytes.NewReader(ukitest.Build(map[string][]byte{".osrel": []byte("ID=x\n")}))); err == nil || !strings.Contains(err.Error(), ".cmdline") {
		t.Errorf("a UKI without a command line: %v", err)
	}
	if _, err := Read(bytes.NewReader([]byte("not a PE"))); err == nil {
		t.Error("read something that is not a PE")
	}
}

func TestUsrHash(t *testing.T) {
	for _, cmdline := range []string{"", "usrhash=abc", "usrhash=" + rootHash + " usrhash=" + rootHash, "usrhash=" + rootHash[:62]} {
		if _, err := UsrHash(cmdline); err == nil {
			t.Errorf("UsrHash(%q) accepted", cmdline)
		}
	}
}

func TestArchitecture(t *testing.T) {
	img := ukitest.Build(map[string][]byte{".text": []byte("x")})
	for _, tc := range []struct {
		machine uint16
		want    string
	}{{0x8664, "x86-64"}, {0xaa64, "arm64"}, {0x14c, ""}} {
		arch, err := Architecture(bytes.NewReader(ukitest.WithMachine(img, tc.machine)))
		if arch != tc.want || (err != nil) != (tc.want == "") {
			t.Errorf("machine %#x: %q, %v; want %q", tc.machine, arch, err, tc.want)
		}
	}
	if _, err := Architecture(strings.NewReader("MZ")); err == nil {
		t.Error("took a file that is no PE image")
	}
	for goarch, want := range map[string]string{"amd64": "x86-64", "arm64": "arm64", "riscv64": ""} {
		if got := GoArchitecture(goarch); got != want {
			t.Errorf("GoArchitecture(%s) = %q, want %q", goarch, got, want)
		}
	}
	for arch, want := range map[string]string{"x86-64": "BOOTX64.EFI", "arm64": "BOOTAA64.EFI", "amd64": ""} {
		if got := BootLoaderName(arch); got != want {
			t.Errorf("BootLoaderName(%s) = %q, want %q", arch, got, want)
		}
	}
}

func TestCheckImageVersion(t *testing.T) {
	for _, tc := range []struct {
		name      string
		osRelease map[string]string
		want      string
	}{
		{"empty", map[string]string{"IMAGE_VERSION": ""}, ""},
		{"set", map[string]string{"IMAGE_VERSION": "0.3.0"}, `sets IMAGE_VERSION to "0.3.0"`},
		{"missing", map[string]string{}, "has no IMAGE_VERSION"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Image{OSRelease: tc.osRelease}.CheckImageVersion()
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("CheckImageVersion() = %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Errorf("CheckImageVersion() = %v, want %q", err, tc.want)
			}
		})
	}
}
