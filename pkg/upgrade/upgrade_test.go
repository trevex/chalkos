package upgrade

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestUpgradeAndFallBack installs a newer image, boots it until its tries are used up, and
// finds the disk booting the image before; once blessed, a boot of the new image is final.
func TestUpgradeAndFallBack(t *testing.T) {
	old, img := newImage(t, "0.1.0", 3), newImage(t, "0.2.0", 2)
	l := newLab(t, old)
	oldUKI := l.file("chalkos_0.1.0.efi")
	rescue := newUKI(map[string]string{"IMAGE_ID": "rescue", "IMAGE_VERSION": "1"}, old.root)
	os.WriteFile(filepath.Join(l.esp, linuxDir, "rescue_1.efi"), rescue, 0o644)
	res, err := l.install(img)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry != "chalkos_0.2.0+2.efi" || !res.Wrote {
		t.Errorf("result %+v", res)
	}
	parts := l.table()
	if parts[4].Name != "store-verity_0.2.0" || parts[5].Name != "store_0.2.0" || parts[2].Name != "store-verity_0.1.0" || parts[3].Name != "store_0.1.0" {
		t.Errorf("labels %q %q %q %q", parts[2].Name, parts[3].Name, parts[4].Name, parts[5].Name)
	}
	if !bytes.Equal(l.file("chalkos_0.1.0.efi"), oldUKI) {
		t.Error("the running UKI changed")
	}
	l.boots(img, 2)
	if !bytes.Equal(l.file("rescue_1.efi"), rescue) {
		t.Error("the UKI of another image changed")
	}

	// Two boots that are never found healthy use the tries up.
	for range 2 {
		if e := l.attempt(); e.Version != "0.2.0" {
			t.Fatalf("booted %s", e.File)
		}
	}
	if e, version := l.boot(); version != "0.1.0" || e.File != "chalkos_0.1.0.efi" {
		t.Errorf("after the tries the disk boots %s from %s, want 0.1.0", version, e.File)
	}
	if l.file("chalkos_0.2.0+0-2.efi") == nil {
		t.Error("the failed UKI is gone")
	}

	// Installed again, the image is booted and blessed.
	if _, err := l.install(img); err != nil {
		t.Fatal(err)
	}
	l.boots(img, 2)
	l.bless(l.attempt())
	for range 3 {
		if e := l.attempt(); e.File != "chalkos_0.2.0.efi" {
			t.Errorf("a blessed boot booted %s", e.File)
		}
	}
}

// TestDowngrade installs an older image: systemd-boot prefers it over the newer one it runs,
// and falls back to that once the older one's tries are used up.
func TestDowngrade(t *testing.T) {
	newer, older := newImage(t, "0.3.0", 3), newImage(t, "0.2.0", 1)
	l := newLab(t, newer)
	if _, err := l.install(older); err != nil {
		t.Fatal(err)
	}
	l.boots(older, 1)
	l.attempt()
	if e, version := l.boot(); version != "0.3.0" {
		t.Errorf("after the tries the disk boots %s from %s, want 0.3.0", version, e.File)
	}
	if _, err := l.install(older); err != nil {
		t.Fatal(err)
	}
	l.bless(l.attempt())
	if e := l.attempt(); e.File != "chalkos_0.2.0.efi" {
		t.Errorf("after blessing the downgrade the disk boots %s", e.File)
	}
}

// TestUpgradeFromSlotB runs the image of slot B and installs the next into slot A, never
// writing slot B or its UKI.
func TestUpgradeFromSlotB(t *testing.T) {
	a, b, c := newImage(t, "0.1.0", 3), newImage(t, "0.2.0", 3), newImage(t, "0.3.0", 3)
	l := newLab(t, a)
	if _, err := l.install(b); err != nil {
		t.Fatal(err)
	}
	e := l.attempt()
	l.bless(e)
	l.runFrom(b, 4, 5)
	l.writes = map[int]int{}
	bUKI := l.file("chalkos_0.2.0.efi")
	res, err := l.install(c)
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry != "chalkos_0.3.0+3.efi" {
		t.Errorf("entry %s", res.Entry)
	}
	if l.writes[2] != 1 || l.writes[3] != 1 || l.writes[4]+l.writes[5] != 0 {
		t.Errorf("writes %v, want partitions 2 and 3 once", l.writes)
	}
	if l.file("chalkos_0.1.0.efi") != nil {
		t.Error("slot A's old UKI stayed")
	}
	if !bytes.Equal(l.file("chalkos_0.2.0.efi"), bUKI) {
		t.Error("the running UKI changed")
	}
	l.boots(c, 3)
}

var errCrash = errors.New("crash")

// TestInterruptedUpgrade stops an upgrade before each change it makes: the disk boots the running
// image or the new one with all its tries, and the upgrade run again completes, without writing
// the slot again once it held the image.
func TestInterruptedUpgrade(t *testing.T) {
	a, b, c := newImage(t, "0.1.0", 3), newImage(t, "0.2.0", 3), newImage(t, "0.3.0", 3)
	for _, start := range []struct {
		name string
		lab  func(t *testing.T) *lab
		img  image
	}{
		{"from slot A with slot B empty", func(t *testing.T) *lab { return newLab(t, a) }, b},
		// The retirement removes slot A's UKI and labels.
		{"from slot B with slot A holding an image", func(t *testing.T) *lab {
			l := newLab(t, a)
			if _, err := l.install(b); err != nil {
				t.Fatal(err)
			}
			l.bless(l.attempt())
			l.runFrom(b, 4, 5)
			return l
		}, c},
	} {
		t.Run(start.name, func(t *testing.T) {
			var changes []string
			l := start.lab(t)
			l.node.Change = func(what string) error {
				changes = append(changes, what)
				return nil
			}
			if _, err := l.install(start.img); err != nil {
				t.Fatal(err)
			}
			t.Logf("changes: %q", changes)
			written := false
			for i, change := range changes {
				written = written || i > 0 && changes[i-1] == "write the hash tree"
				t.Run(change, func(t *testing.T) {
					l := start.lab(t)
					calls := 0
					l.node.Change = func(string) error {
						calls++
						if calls == i+1 {
							return errCrash
						}
						return nil
					}
					if _, err := l.install(start.img); !errors.Is(err, errCrash) {
						t.Fatalf("install = %v, want the crash", err)
					}
					l.bootsRunningOr(start.img, 3)
					l.node.Change = nil
					res, err := l.install(start.img)
					if err != nil {
						t.Fatal(err)
					}
					if res.Wrote == written {
						t.Errorf("wrote the slot: %v, after the crash at %q", res.Wrote, change)
					}
					l.boots(start.img, 3)
				})
			}
		})
	}
}

// TestInterruptedTransfer cuts the stream at points within the store, the hash tree and the UKI.
func TestInterruptedTransfer(t *testing.T) {
	old, img := newImage(t, "0.1.0", 3), newImage(t, "0.2.0", 3)
	store, hash, uki := int64(len(img.store)), int64(len(img.hash)), int64(len(img.uki))
	for _, cut := range []int64{0, store / 2, store, store + hash/2, store + hash, store + hash + uki/2, store + hash + uki - 1} {
		l := newLab(t, old)
		if _, err := l.node.Install(context.Background(), img.header, io.LimitReader(img.stream(), cut)); err == nil || !strings.Contains(err.Error(), "receive") {
			t.Errorf("a stream cut after %d bytes: %v", cut, err)
		}
		l.bootsRunningOr(img, 3)
		if _, err := l.install(img); err != nil {
			t.Fatal(err)
		}
		l.boots(img, 3)
	}
}

// TestRefusesBeforeChanging checks what is refused before anything changes.
func TestRefusesBeforeChanging(t *testing.T) {
	old, img := newImage(t, "0.1.0", 3), newImage(t, "0.2.0", 3)
	rebuilt := newImage(t, "0.1.1", 3)
	rebuilt.version, rebuilt.header.Version = "0.1.0", "0.1.0"
	for _, tc := range []struct {
		name   string
		header func(h *Header)
		setup  func(l *lab)
		want   string
	}{
		{"another image ID", func(h *Header) { h.ImageID = "other" }, nil, "image ID is other"},
		{"another cluster", func(h *Header) { h.Cluster = "prod" }, nil, "cluster is prod"},
		{"another role", func(h *Header) { h.Role = "controlplane" }, nil, "role is controlplane"},
		{"a version with a counter", func(h *Header) { h.Version = "0.2.0+1" }, nil, "version"},
		{"an upper-case version", func(h *Header) { h.Version = "0.2.0-RC1" }, nil, "version"},
		{"the running version, rebuilt", func(h *Header) { *h = rebuilt.header }, nil, "build the image with a new version"},
		{"a version on the ESP, rebuilt", nil, func(l *lab) {
			os.WriteFile(filepath.Join(l.esp, linuxDir, "chalkos_0.2.0+0-3.efi"), newUKI(img.osRelease(3), old.root), 0o644)
		}, "build the image with a new version"},
		{"a store larger than the slot", func(h *Header) { h.StoreSize = 2 << 20 }, nil, "do not fit"},
		{"a running boot not found healthy", nil, func(l *lab) {
			os.Rename(filepath.Join(l.esp, linuxDir, "chalkos_0.1.0.efi"), filepath.Join(l.esp, linuxDir, "chalkos_0.1.0+2-1.efi"))
			os.WriteFile(filepath.Join(l.esp, linuxDir, "chalkos_0.0.9.efi"), newImage(t, "0.0.9", 3).uki, 0o644)
		}, "not been found healthy"},
		{"an entry that boots another store", nil, func(l *lab) { l.setVariable("LoaderEntrySelected", "chalkos_0.0.9.efi") }, "not on the ESP"},
		{"no second slot", nil, func(l *lab) {
			if out, err := exec.Command("sfdisk", "--delete", l.disk, "5").CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
		}, "needs two slots"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t, old)
			if tc.setup != nil {
				tc.setup(l)
			}
			before := l.table()
			l.node.Change = func(what string) error {
				t.Errorf("changed: %s", what)
				return errCrash
			}
			h := img.header
			if tc.header != nil {
				tc.header(&h)
			}
			_, err := l.node.Install(context.Background(), h, img.stream())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("install = %v, want %q", err, tc.want)
			}
			for n, p := range l.table() {
				if before[n] != p {
					t.Errorf("partition %d changed", n)
				}
			}
		})
	}

	l := newLab(t, old)
	l.node.Change = func(what string) error {
		t.Errorf("changed: %s", what)
		return errCrash
	}
	if res, err := l.install(old); err != nil || !res.AlreadyInstalled {
		t.Errorf("installing the running image: %+v, %v", res, err)
	}
}

// TestRefusesBeforeActivating checks what is refused once the image arrived: the slot stays
// retired and the disk boots the running image.
func TestRefusesBeforeActivating(t *testing.T) {
	old, img := newImage(t, "0.1.0", 3), newImage(t, "0.2.0", 3)
	flip := func(b []byte, i int) []byte {
		c := bytes.Clone(b)
		c[i] ^= 1
		return c
	}
	otherVersion := img
	otherVersion.uki = newImage(t, "0.3.0", 3).uki
	otherStore := img
	otherStore.uki = newUKI(img.osRelease(3), old.root)
	otherRole := img
	osRelease := img.osRelease(3)
	osRelease["CHALKOS_ROLE"] = "controlplane"
	otherRole.uki = newUKI(osRelease, img.root)
	noTries := img
	noTries.uki = newUKI(img.osRelease(0), img.root)
	wrongRoot := img
	wrongRoot.root = old.root
	for _, tc := range []struct {
		name string
		img  image
		want string
	}{
		{"a corrupted store", func() image { c := img; c.store = flip(img.store, 100); return c }(), "store's SHA-256"},
		{"a corrupted hash tree", func() image { c := img; c.hash = flip(img.hash, 600); return c }(), "hash tree's SHA-256"},
		{"a store that does not match the root hash", withHeader(wrongRoot), "root hash"},
		{"a corrupted UKI", func() image { c := img; c.uki = flip(img.uki, 700); return c }(), "UKI's SHA-256"},
		{"a UKI of another version", withHeader(otherVersion), "version is \"0.3.0\""},
		{"a UKI of another role", withHeader(otherRole), "role is \"controlplane\""},
		{"a UKI booting another store", withHeader(otherStore), "boots another store"},
		{"a UKI without boot tries", withHeader(noTries), "boot tries"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t, old)
			_, err := l.node.Install(context.Background(), tc.img.header, io.MultiReader(bytes.NewReader(tc.img.store), bytes.NewReader(tc.img.hash), bytes.NewReader(tc.img.uki)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("install = %v, want %q", err, tc.want)
			}
			if e, version := l.boot(); version != "0.1.0" {
				t.Errorf("the disk boots %s from %s", version, e.File)
			}
			if p := l.table(); p[4].Name != emptyLabel || p[5].Name != emptyLabel {
				t.Errorf("the slot is labelled %q and %q", p[4].Name, p[5].Name)
			}
		})
	}
}

// withHeader gives an image the header of its store and UKI as they are.
func withHeader(img image) image {
	img.header = img.headerFor(img.uki)
	img.header.RootHash = img.root
	return img
}

// TestSecureBoot checks the UKI against the firmware's db when Secure Boot is on.
func TestSecureBoot(t *testing.T) {
	if _, err := exec.LookPath("sbsign"); err != nil {
		t.Skip("sbsign not in PATH")
	}
	old, img := newImage(t, "0.1.0", 3), newImage(t, "0.2.0", 3)
	dir := t.TempDir()
	key, cert, certDER := testSigner(t, dir, "db")
	_, _, otherDER := testSigner(t, dir, "other")
	signed := img
	signed.uki = sbsign(t, dir, img.uki, key, cert)
	signed.header = signed.headerFor(signed.uki)

	for _, tc := range []struct {
		name       string
		secureBoot byte
		db         []byte
		img        image
		want       string
	}{
		{"unsigned", 1, certDER, img, "Secure Boot would refuse the UKI: the image is not signed"},
		{"signed by another key", 1, otherDER, signed, "not in db"},
		{"signed", 1, certDER, signed, ""},
		{"Secure Boot off", 0, otherDER, img, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newLab(t, old)
			l.setGlobal("SecureBoot", globalVendor, []byte{tc.secureBoot})
			l.setGlobal("SetupMode", globalVendor, []byte{0})
			l.setGlobal("db", securityVendor, signatureList(tc.db))
			l.setGlobal("dbx", securityVendor, nil)
			_, err := l.install(tc.img)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				l.boots(img, 3)
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("install = %v, want %q", err, tc.want)
			}
			if _, version := l.boot(); version != "0.1.0" {
				t.Errorf("the disk boots %s", version)
			}
		})
	}
}

// signatureList is an EFI signature list of one X.509 certificate.
func signatureList(der []byte) []byte {
	b := make([]byte, 28)
	copy(b, []byte{0xa1, 0x59, 0xc0, 0xa5, 0xe4, 0x94, 0xa7, 0x4a, 0x87, 0xb5, 0xab, 0x15, 0x5c, 0x2b, 0xf0, 0x72})
	binary.LittleEndian.PutUint32(b[16:], uint32(28+16+len(der)))
	binary.LittleEndian.PutUint32(b[24:], uint32(16+len(der)))
	return append(append(b, make([]byte, 16)...), der...)
}

func testSigner(t *testing.T, dir, name string) (key, cert string, der []byte) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err = x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	key, cert = filepath.Join(dir, name+".key"), filepath.Join(dir, name+".crt")
	os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0o600)
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	return key, cert, der
}

func sbsign(t *testing.T, dir string, image []byte, key, cert string) []byte {
	t.Helper()
	in, out := filepath.Join(dir, "in.efi"), filepath.Join(dir, "out.efi")
	if err := os.WriteFile(in, image, 0o644); err != nil {
		t.Fatal(err)
	}
	if o, err := exec.Command("sbsign", "--key", key, "--cert", cert, "--output", out, in).CombinedOutput(); err != nil {
		t.Fatalf("sbsign: %v: %s", err, o)
	}
	signed, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
