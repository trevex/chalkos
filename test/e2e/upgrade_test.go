package e2e

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/trevex/chalkos/pkg/lab"
	"github.com/trevex/chalkos/pkg/ociimage"
)

// TestUpgrade installs the signed test image under Secure Boot, which boots its UKI as
// chalkos_1.efi, upgrades it to the upgrade image, which boots from slot B as chalkos_2.efi with
// STATE and VAR unlocked by the TPM and is found healthy, and then to the unhealthy image,
// installed as chalkos_3, which never becomes healthy: after its one try the node falls back to
// chalkos_2.efi and says so. Each image is named by its root hash. The node pulls the upgrade image
// from a registry on the host with the credentials of a Registry the upgrade delivers; the upgrade
// to the unhealthy image still streams it from its directory. The upgrade carries config only the
// upgrade image accepts, a Farewell, and the upgrade to the unhealthy image the test image's config
// again: the fallback boots the upgrade image with its own config, Farewell included.
func TestUpgrade(t *testing.T) {
	requireEnv(t, append(secureBootEnv, append(chalkdEnv, "CHALKLAB_UPGRADE_IMAGE_DIR", "CHALKLAB_UNHEALTHY_IMAGE_DIR")...)...)
	base, nextHash, badHash := rootHash(t, "CHALKLAB_IMAGE_DIR"), rootHash(t, "CHALKLAB_UPGRADE_IMAGE_DIR"), rootHash(t, "CHALKLAB_UNHEALTHY_IMAGE_DIR")
	start := time.Now()
	dir := vmDir(t)
	reg := startImageRegistry(t, filepath.Join(dir, "registry"), "pull-s3cret")
	disk := prepareDisk(t, dir, diskOpts{sign: true})
	n := startNode(t, lab.VMConfig{
		Dir: dir, FirmwareVars: os.Getenv("CHALKLAB_OVMF_VARS_ENROLLED"), Disks: []lab.Disk{disk},
		GuestForwards: []lab.GuestForward{{Guest: registryGuest, Host: reg.host}},
	})
	readFacts(t, n.vm, 5*time.Minute)
	installInPlace(t, n, "chalklab")
	assertFacts(t, readFacts(t, n.vm, 5*time.Minute), map[string]string{
		"secureboot":      "1",
		"store_label":     "store_" + base[:16],
		"store_partition": "3",
		"state_boots":     "2",
		"var_boots":       "1",
	})
	waitForNode(t, n, "chalklab")
	if out, err := chalkctl(t, n, "base", "status", "chalklab"); err != nil || !strings.Contains(out, "image "+base[:12]+", booted from chalkos_1.efi\n") {
		t.Errorf("status after the install: %v", err)
	}
	t.Logf("installed after %v", time.Since(start).Round(time.Second))

	nextImage, badImage := signedImage(t, dir, "CHALKLAB_UPGRADE_IMAGE_DIR"), signedImage(t, dir, "CHALKLAB_UNHEALTHY_IMAGE_DIR")
	pullInventory, layers := publish(t, reg, nextImage, ociInventory{
		base: "upgrade", out: "upgrade-oci.yaml", directory: "images/test-upgrade-metal", repo: "chalkos/test-upgrade",
		files: "nodes/chalklab/farewell.yaml", registry: "nodes/chalklab/registry.yaml",
	})
	pinnedPrefix := registryGuest + "/chalkos/test-upgrade@sha256:"
	upgraded := time.Now()
	out, err := chalkctl(t, nil, "", "upgrade", "--inventory", pullInventory, "--nodes", "chalklab", "--address", "chalklab="+n.addr, "--timeout", "10m")
	if err != nil || !strings.Contains(out, "chalklab: config 3 added, 0 removed, 0 changed\n") ||
		!strings.Contains(out, "chalklab: installing "+nextHash[:12]+", which it pulls from "+pinnedPrefix) ||
		!strings.Contains(out, "chalklab: installed "+nextHash[:12]+", which boots next as chalkos_2+3.efi") ||
		!strings.Contains(out, "upgraded 1 node to "+nextHash[:12]) {
		t.Fatalf("upgrade to %s: %v", nextHash[:12], err)
	}
	if _, ok := reg.served.Load(layers[ociimage.StoreType]); !ok {
		t.Error("the node did not pull the store from the registry")
	}
	t.Logf("pulled and upgraded to %s in %v", nextHash[:12], time.Since(upgraded).Round(time.Second))
	// Slot B, under Secure Boot, with the TPM unsealing STATE and VAR as before.
	assertFacts(t, readFacts(t, n.vm, time.Minute), map[string]string{
		"secureboot":      "1",
		"usr_verity":      "verified",
		"store_label":     "store_" + nextHash[:16],
		"store_partition": "5",
		"slot_b_empty":    "0",
		"state_tpm2":      "1",
		"var_tpm2":        "1",
		"state_boots":     "3",
		"var_boots":       "2",
	})
	if out, err := chalkctl(t, n, "", "status", "chalklab", "--inventory", pullInventory); err != nil || !strings.Contains(out, "image "+nextHash[:12]+", booted from chalkos_2.efi\n") || !inventoryConfig.MatchString(out) {
		t.Errorf("status after the upgrade: %v", err)
	}

	failed := time.Now()
	out, err = chalkctl(t, nil, "base", "upgrade", "--image", badImage, "--nodes", "chalklab", "--address", "chalklab="+n.addr, "--timeout", "10m")
	if err == nil || !strings.Contains(out, `chalklab: config 0 added, 3 removed (Registry "e2e", Secret "registry", Farewell "bye"), 0 changed`+"\n") ||
		!strings.Contains(out, "chalklab: installed "+badHash[:12]+", which boots next as chalkos_3+1.efi") ||
		!strings.Contains(out, "chalklab: upgrade to "+badHash[:12]+" failed: rolled back to "+nextHash[:12]) || !strings.Contains(out, "units failed: chalktest-broken.service") {
		t.Fatalf("upgrade to %s: %v", badHash[:12], err)
	}
	t.Logf("fell back from %s in %v", badHash[:12], time.Since(failed).Round(time.Second))
	// One boot of the unhealthy image from slot A, then the upgrade image again.
	assertFacts(t, readFacts(t, n.vm, time.Minute), map[string]string{"store_label": "store_" + badHash[:16], "store_partition": "3", "state_boots": "4"})
	assertFacts(t, readFacts(t, n.vm, time.Minute), map[string]string{"store_label": "store_" + nextHash[:16], "store_partition": "5", "state_boots": "5", "var_boots": "4"})
	// The upgrade image runs the config the upgrade gave it, not the one sent with the unhealthy image.
	out, err = chalkctl(t, n, "", "status", "chalklab", "--inventory", pullInventory)
	if err != nil || !strings.Contains(out, "image "+nextHash[:12]+", booted from chalkos_2.efi\n") || !strings.Contains(out, "upgrade to "+badHash[:12]+" failed: rolled back to "+nextHash[:12]) ||
		!strings.Contains(out, "units failed: chalktest-broken.service") || !inventoryConfig.MatchString(out) {
		t.Errorf("status after the rollback: %v", err)
	}
	t.Logf("the test took %v", time.Since(start).Round(time.Second))
}

// inventoryConfig is status's line of a node that runs the config of the inventory it was given.
var inventoryConfig = regexp.MustCompile(`(?m)^config [0-9a-f]{64} \(the inventory's\)$`)

// replaceOnce replaces the one old in s, failing the test when s does not hold it once.
func replaceOnce(t *testing.T, s, old, new string) string {
	t.Helper()
	if strings.Count(s, old) != 1 {
		t.Fatalf("%q is not once in:\n%s", old, s)
	}
	return strings.Replace(s, old, new, 1)
}

// ociInventory says how publish derives, from one of the fixtures' inventories, an inventory that
// names an image in the registry.
type ociInventory struct {
	// base names the fixtures' inventory, as inventories names it, and out the file publish writes
	// beside it.
	base, out string
	// directory is the image directory whose source the image in the registry replaces, and repo
	// the repository the image is pushed to, as repo:v1.
	directory, repo string
	// files is the last file of the node's list, which registry, the file publish writes the
	// Registry and its Secret to, follows; both are relative to the inventory.
	files, registry string
}

// publish pushes the image directory to the registry and writes the inventory o describes. The
// node gets a Registry with the registry's credentials, which allows plain HTTP to it. chalkctl on
// the host reaches the registry at the node's address of it through the registry itself as its
// HTTP proxy, as the host has no route to 10.0.2.100, and authenticates with a docker config. It
// returns the inventory and the digests of the image's store and boot-loader layers.
func publish(t *testing.T, reg *imageRegistry, imageDir string, o ociInventory) (string, map[types.MediaType]string) {
	t.Helper()
	img, closeImage, err := ociimage.FromDirectory(context.Background(), imageDir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeImage()
	reg.push(t, img, o.repo, "v1")
	layers := map[types.MediaType]string{}
	for _, mt := range []types.MediaType{ociimage.StoreType, ociimage.BootLoaderType} {
		l, err := ociimage.Layer(img, mt)
		if err != nil {
			t.Fatal(err)
		}
		d, err := l.Digest()
		if err != nil {
			t.Fatal(err)
		}
		layers[mt] = d.String()
	}
	docker := t.TempDir()
	auth := base64.StdEncoding.EncodeToString([]byte("e2e:" + reg.password))
	if err := os.WriteFile(filepath.Join(docker, "config.json"), []byte(`{"auths": {"`+registryGuest+`": {"auth": "`+auth+`"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", docker)
	t.Setenv("HTTP_PROXY", "http://"+reg.host)
	registryDocs := `apiVersion: chalkos.dev/v1alpha1
kind: Registry
metadata: {name: e2e}
spec: {host: "` + registryGuest + `", insecure: true, username: e2e, password: {secretRef: {name: registry, key: password}}}
---
apiVersion: chalkos.dev/v1alpha1
kind: Secret
metadata: {name: registry}
spec: {data: {password: "` + reg.password + `"}}
`
	base := inventoryPath(o.base)
	dir := filepath.Dir(base)
	if err := os.WriteFile(filepath.Join(dir, o.registry), []byte(registryDocs), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	inv := replaceOnce(t, string(data), "{source: {directory: "+o.directory+"}}", `{source: {oci: "`+registryGuest+"/"+o.repo+`:v1"}}`)
	inv = replaceOnce(t, inv, o.files+"]", o.files+", "+o.registry+"]")
	path := filepath.Join(dir, o.out)
	if err := os.WriteFile(path, []byte(inv), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, layers
}
