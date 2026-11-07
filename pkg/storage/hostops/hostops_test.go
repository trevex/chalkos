package hostops_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/storage/hostops"
	"github.com/trevex/chalkos/pkg/storage/hostops/hostopstest"
	"github.com/trevex/chalkos/pkg/tool"
	"github.com/trevex/chalkos/pkg/tool/tooltest"
)

// The tools run with exactly these command lines.
func TestToolsCommandLines(t *testing.T) {
	ctx := context.Background()
	r := &tooltest.Runner{}
	h := hostops.Tools{Run: r}
	for _, err := range []error{
		h.Mount(ctx, "/dev/vda6", "/state", "ext4", ""),
		h.Mount(ctx, "/dev/vda1", "/esp", "vfat", "umask=0077"),
		h.Unmount(ctx, "/state"),
		h.AttachLUKS(ctx, "var", "/dev/vda7", hostops.LUKSOptions{}),
		h.AttachLUKS(ctx, "state", "/dev/vda6", hostops.LUKSOptions{Prompt: true}),
		h.AttachLUKS(ctx, "state", "/dev/vdb6", hostops.LUKSOptions{ReadOnly: true}),
		h.DetachLUKS(ctx, "state"),
		h.OpenVerity(ctx, "store", "/dev/vda3", "/dev/vda2", []byte{0xab, 0xcd}),
		h.CloseVerity(ctx, "store"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"mount -t ext4 /dev/vda6 /state",
		"mount -t vfat -o umask=0077 /dev/vda1 /esp",
		"umount /state",
		"systemd-cryptsetup attach var /dev/vda7 - tpm2-device=auto,headless=true",
		"systemd-cryptsetup attach state /dev/vda6 - tpm2-device=auto",
		"systemd-cryptsetup attach state /dev/vdb6 - tpm2-device=auto,headless=true,read-only",
		"systemd-cryptsetup detach state",
		"veritysetup open /dev/vda3 store /dev/vda2 abcd",
		"veritysetup close store",
	}
	if !slices.Equal(r.Calls, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(r.Calls, "\n"), strings.Join(want, "\n"))
	}
}

func TestToolsReadTheHost(t *testing.T) {
	dir := t.TempDir()
	mountInfo := filepath.Join(dir, "mountinfo")
	if err := os.WriteFile(mountInfo, []byte("30 1 0:27 / / rw - tmpfs tmpfs rw\n41 30 253:1 / /state rw,relatime - ext4 /dev/mapper/state rw\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "dev", "mapper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dev", "mapper", "state"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := hostops.Tools{MountInfo: mountInfo, DevRoot: filepath.Join(dir, "dev")}
	for path, want := range map[string]bool{"/state": true, "/": true, "/var": false} {
		if got, err := h.Mounted(path); err != nil || got != want {
			t.Errorf("Mounted(%s) = %v, %v, want %v", path, got, err, want)
		}
	}
	if !h.Mapped("state") || h.Mapped("var") {
		t.Errorf("Mapped(state) = %v, Mapped(var) = %v", h.Mapped("state"), h.Mapped("var"))
	}
	if _, err := (hostops.Tools{MountInfo: filepath.Join(dir, "none")}).Mounted("/"); err == nil {
		t.Error("an unreadable mount table is no error")
	}
}

func TestRelease(t *testing.T) {
	ctx := context.Background()
	r := &tooltest.Runner{}
	f := hostopstest.New(r)
	f.SetMounted("/dev/mapper/state", "/state")
	f.SetMapped("state", "/dev/vda6")
	if err := hostops.Release(ctx, f, "STATE", "/state", "state", f.DetachLUKS); err != nil {
		t.Fatal(err)
	}
	if want := []string{"umount /state", "systemd-cryptsetup detach state"}; !slices.Equal(r.Calls, want) {
		t.Errorf("ran %q, want %q", r.Calls, want)
	}
	if f.Holds("/dev/vda") {
		t.Error("STATE is still held")
	}

	// Nothing left: nothing runs.
	r.ResetCalls()
	if err := hostops.Release(ctx, f, "STATE", "/state", "state", f.DetachLUKS); err != nil || len(r.Calls) != 0 {
		t.Errorf("Release = %v, ran %q", err, r.Calls)
	}

	r.Rules = []tooltest.Rule{{Prefix: "veritysetup close", Err: &tool.Error{Command: "veritysetup", Code: 5, Stderr: "busy"}}}
	f.SetMapped("store", "/dev/vda3")
	if err := hostops.Release(ctx, f, "the checked store", "/store", "store", f.CloseVerity); err == nil || !strings.HasPrefix(err.Error(), "close the checked store: ") {
		t.Errorf("Release = %v, want the close's error", err)
	}
}

// An unmount that fails is no error when the file system is gone from the mount table since it
// was read, whatever umount said; one still mounted is.
func TestReleaseAFailedUnmount(t *testing.T) {
	ctx := context.Background()
	mountInfo := filepath.Join(t.TempDir(), "mountinfo")
	const esp = "42 30 259:1 / /esp rw - vfat /dev/vda1 rw\n"
	for _, c := range []struct {
		name  string
		after string
		fails bool
	}{
		{"unmounted meanwhile", "", false},
		{"still mounted", esp, true},
	} {
		if err := os.WriteFile(mountInfo, []byte(esp), 0o644); err != nil {
			t.Fatal(err)
		}
		r := &tooltest.Runner{
			Rules: []tooltest.Rule{{Prefix: "umount", Err: &tool.Error{Command: "umount", Code: 32, Stderr: "umount: /esp: Ziel ist nicht eingehängt."}}},
			Before: func(string) {
				if err := os.WriteFile(mountInfo, []byte(c.after), 0o644); err != nil {
					t.Error(err)
				}
			},
		}
		err := hostops.Release(ctx, hostops.Tools{Run: r, MountInfo: mountInfo}, "the ESP", "/esp", "", nil)
		if c.fails && (err == nil || !strings.HasPrefix(err.Error(), "unmount the ESP: ")) || !c.fails && err != nil {
			t.Errorf("%s: Release = %v", c.name, err)
		}
		if !slices.Equal(r.Calls, []string{"umount /esp"}) {
			t.Errorf("%s: ran %q", c.name, r.Calls)
		}
	}
}

func TestContent(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name  string
		rule  tooltest.Rule
		want  string
		fails bool
	}{
		{"LUKS", tooltest.Rule{Prefix: "blkid", Out: "crypto_LUKS\n"}, hostops.ContentLUKS, false},
		{"nothing", tooltest.Rule{Prefix: "blkid", Err: &tool.Error{Command: "blkid", Code: 2}}, "", false},
		{"unreadable", tooltest.Rule{Prefix: "blkid", Err: &tool.Error{Command: "blkid", Code: 2, Stderr: "Input/output error"}}, "", true},
	} {
		r := &tooltest.Runner{Rules: []tooltest.Rule{c.rule}}
		got, err := hostops.Content(ctx, r, "/dev/vda6")
		if got != c.want || (err != nil) != c.fails {
			t.Errorf("%s: Content = %q, %v", c.name, got, err)
		}
		if want := []string{"udevadm settle --timeout=30", "blkid -p -o value -s TYPE /dev/vda6"}; !slices.Equal(r.Calls, want) {
			t.Errorf("%s: ran %q", c.name, r.Calls)
		}
		// Type takes finding nothing for an error.
		if _, err := hostops.Type(ctx, r, "/dev/vda6"); (err != nil) != (c.want == "") {
			t.Errorf("%s: Type = %v", c.name, err)
		}
	}
}
