package inventory

import (
	"path/filepath"
	"strings"
	"testing"
)

// images is an inventory whose nodes take their images from each place one may be named: cp1
// names its own over its group's and spec.all.imageName, w1 takes its group's over
// spec.all.imageName, w2 is in two groups that name the same image, and w3 takes
// spec.all.imageName.
const images = `apiVersion: chalkctl.chalkos.dev/v1alpha1
kind: Inventory
metadata: {name: homelab}
spec:
  images:
    node-metal: {source: {directory: images/node-metal}}
    node-gpu: {source: {directory: ../builds/./node-gpu}}
    node-kvm: {source: {directory: /srv/images/../images/node-kvm}}
  all: {imageName: node-metal}
  groups:
    gpu: {imageName: node-gpu}
    gpu-too: {imageName: node-gpu}
    rack: {}
  nodes:
    cp1: {imageName: node-kvm, groups: [gpu]}
    w1: {groups: [rack, gpu]}
    w2: {groups: [gpu, gpu-too]}
    w3: {groups: [rack]}
`

// TestImage gives a node its own image, else its groups', else spec.all.imageName, with the
// directory the inventory names for it: a relative one against the inventory's directory, an
// absolute one as written.
func TestImage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "lab")
	write(t, filepath.Join(dir, "chalkos.yaml"), images)
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for node, want := range map[string]struct{ name, dir string }{
		"cp1": {"node-kvm", "/srv/images/node-kvm"},
		"w1":  {"node-gpu", filepath.Join(filepath.Dir(dir), "builds", "node-gpu")},
		"w2":  {"node-gpu", filepath.Join(filepath.Dir(dir), "builds", "node-gpu")},
		"w3":  {"node-metal", filepath.Join(dir, "images", "node-metal")},
	} {
		name, imageDir, err := inv.Image(node)
		if err != nil || name != want.name || imageDir != want.dir {
			t.Errorf("image of %s = %s, %s, %v; want %s, %s", node, name, imageDir, err, want.name, want.dir)
		}
	}
	if _, _, err := inv.Image("w9"); err == nil || !strings.Contains(err.Error(), "has no node w9") {
		t.Errorf("image of w9 = %v", err)
	}
}

// TestNodeTakesItsImage gives a node's config the image the inventory names for it, whatever
// its files hold.
func TestNodeTakesItsImage(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "chalkos.yaml"), strings.Replace(images, "    cp1: {imageName: node-kvm, groups: [gpu]}", "    cp1: {imageName: node-kvm, groups: [gpu], files: [cp1.yaml]}", 1))
	write(t, filepath.Join(dir, "cp1.yaml"), nodeFile("cp1", "controlplane", "10.0.0.11"))
	inv, err := Load(filepath.Join(dir, "chalkos.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cp1, err := inv.Node("cp1")
	if err != nil {
		t.Fatal(err)
	}
	if cp1.ImageName != "node-kvm" || cp1.ImageDir != "/srv/images/node-kvm" {
		t.Errorf("cp1 runs %s in %s", cp1.ImageName, cp1.ImageDir)
	}
}

// TestLoadRefusesImages refuses a node without an image, one in two groups that name different
// images, even when the node names its own, and an image the inventory does not define, wherever
// it is named.
func TestLoadRefusesImages(t *testing.T) {
	for name, tc := range map[string]struct{ old, new, want string }{
		"no image": {"  all: {imageName: node-metal}\n", "",
			"node w3 has no image; name one in spec.all.imageName, in a group of the node or on the node"},
		"two groups of different images": {"gpu-too: {imageName: node-gpu}", "gpu-too: {imageName: node-metal}",
			"node w2 is in the groups gpu and gpu-too, which name different images: node-gpu and node-metal"},
		"two groups of different images of a node with its own": {"    rack: {}\n  nodes:\n    cp1: {imageName: node-kvm, groups: [gpu]}", "    rack: {}\n    metal: {imageName: node-metal}\n  nodes:\n    cp1: {imageName: node-kvm, groups: [gpu, metal]}",
			"node cp1 is in the groups gpu and metal, which name different images: node-gpu and node-metal"},
		"an undefined top-level image": {"all: {imageName: node-metal}", "all: {imageName: node-arm}",
			"spec.all.imageName names the image node-arm, which the inventory does not define"},
		"an undefined group image": {"rack: {}", "rack: {imageName: node-arm}",
			"the group rack names the image node-arm, which the inventory does not define"},
		"an undefined node image": {"cp1: {imageName: node-kvm", "cp1: {imageName: node-arm",
			"node cp1 names the image node-arm, which the inventory does not define"},
		"an image without a directory": {"node-metal: {source: {directory: images/node-metal}}", "node-metal: {source: {}}",
			"directory"},
		"an image name in caps": {"    node-metal: {source:", "    Node-metal: {source:", "Node-metal"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "chalkos.yaml")
			if !strings.Contains(images, tc.old) {
				t.Fatalf("the inventory holds no %q", tc.old)
			}
			write(t, path, strings.Replace(images, tc.old, tc.new, 1))
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Load = %v, want an error naming %q", err, tc.want)
			}
		})
	}
}
