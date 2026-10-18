package chalklab

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/lab"
)

func testApp() (*app, *bytes.Buffer) {
	var out bytes.Buffer
	return &app{stdin: strings.NewReader(""), stdout: &out, stderr: io.Discard,
		output:   func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not in the test") },
		chalkctl: func(context.Context, io.Writer, ...string) error { return errors.New("not in the test") },
	}, &out
}

// writeLabFiles writes an inventory of cp1 and w1, whose image node-kvm is images/node-kvm, and
// m1, whose image node-metal is a metal image, and the lab file given; it returns the lab file's
// path.
func writeLabFiles(t *testing.T, dir, labFile string) string {
	t.Helper()
	files := map[string]string{
		"chalkos.yaml": "apiVersion: chalkctl.chalkos.dev/v1alpha1\nkind: Inventory\nmetadata: {name: lab}\nspec:\n" +
			"  images:\n    node-kvm: {source: {directory: images/node-kvm}}\n    node-metal: {source: {directory: images/node-metal}}\n  image: node-kvm\n" +
			"  files: [cluster.yaml]\n  nodes:\n" +
			"    cp1: {address: 192.168.123.11, files: [cp1.yaml]}\n    w1: {address: 192.168.123.12, files: [w1.yaml]}\n    m1: {image: node-metal, files: [m1.yaml]}\n",
		"cluster.yaml":  "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Kubernetes\nmetadata: {name: kubernetes}\nspec: {endpoint: \"https://192.168.123.11:6443\"}\n",
		"cp1.yaml":      "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: default}\nspec: {type: controlplane}\n",
		"w1.yaml":       "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: default}\nspec: {type: worker}\n",
		"m1.yaml":       "apiVersion: kubernetes.chalkos.dev/v1alpha1\nkind: Node\nmetadata: {name: default}\nspec: {type: worker}\n",
		"chalklab.yaml": labFile,
	}
	for _, p := range []string{"kvm", "metal"} {
		files["images/node-"+p+"/metadata.json"] = `{"schemaVersion": 0, "id": "chalkos", "variant": "node", "platform": "` + p + `", "rootHash": "` + strings.Repeat("ab", 32) + `", "kinds": []}`
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "chalklab.yaml")
}

// testLab runs cp1 with the defaults of a control plane, and w1 with its own machine.
const testLab = `apiVersion: chalklab.chalkos.dev/v1alpha1
kind: Lab
metadata: {name: lab}
spec:
  inventory: ./chalkos.yaml
  nodes:
    cp1: {mac: "52:54:00:7B:00:11"}
    w1: {mac: "52:54:00:7b:00:12", memory: 1024, cpus: 1, disks: [{size: 20G}, {size: 1G, serial: chalk-data}]}
`

// TestLabNodes runs the nodes of the lab file, each with its MAC address and the machine its type
// gives it unless the lab file says otherwise, from the directory of its image in the inventory; it
// refuses a node of a metal image, two nodes of one MAC address, a multicast or broadcast MAC
// address, and a node the inventory lacks.
func TestLabNodes(t *testing.T) {
	dir := t.TempDir()
	lf, err := readLabFile(writeLabFiles(t, dir, testLab))
	if err != nil {
		t.Fatal(err)
	}
	inv, err := inventory.Load(lf.inventory)
	if err != nil {
		t.Fatal(err)
	}
	nodes, images, err := labNodes(inv, lf)
	if err != nil {
		t.Fatal(err)
	}
	want := []lab.LabNode{
		{Name: "cp1", Kind: "controlplane", MAC: "52:54:00:7b:00:11", MemoryMB: 3072, CPUs: 2, Disks: []lab.LabDisk{{Size: "16G"}}},
		{Name: "w1", Kind: "worker", MAC: "52:54:00:7b:00:12", MemoryMB: 1024, CPUs: 1, Disks: []lab.LabDisk{{Size: "20G"}, {Size: "1G", Serial: "chalk-data"}}},
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Errorf("nodes = %+v, want %+v", nodes, want)
	}
	if images["cp1"] != filepath.Join(dir, "images", "node-kvm") {
		t.Errorf("images = %v", images)
	}
	for name, tc := range map[string]struct{ lab, want string }{
		"a metal image":     {strings.Replace(testLab, "    w1:", "    m1: {mac: \"52:54:00:7b:00:13\"}\n    w1:", 1), "chalklab runs kvm images, but the image node-metal of m1 is built for metal"},
		"one MAC address":   {strings.Replace(testLab, "52:54:00:7b:00:12", "52:54:00:7b:00:11", 1), "the same MAC address 52:54:00:7b:00:11"},
		"a multicast MAC":   {strings.Replace(testLab, "52:54:00:7b:00:12", "01:00:5e:00:00:12", 1), "the MAC address 01:00:5e:00:00:12 of w1 is a multicast address"},
		"the broadcast MAC": {strings.Replace(testLab, "52:54:00:7b:00:12", "ff:ff:ff:ff:ff:ff", 1), "the MAC address ff:ff:ff:ff:ff:ff of w1 is a multicast address"},
		"no inventory node": {strings.Replace(testLab, "    w1:", "    x9: {mac: \"52:54:00:7b:00:19\"}\n    w1:", 1), "has no node x9"},
	} {
		t.Run(name, func(t *testing.T) {
			lf, err := readLabFile(writeLabFiles(t, t.TempDir(), tc.lab))
			if err != nil {
				t.Fatal(err)
			}
			inv, err := inventory.Load(lf.inventory)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := labNodes(inv, lf); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("labNodes = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestReadLabFileKeepsTheName keeps the lab file's metadata.name, which names the lab's state
// directory.
func TestReadLabFileKeepsTheName(t *testing.T) {
	lf, err := readLabFile(writeLabFiles(t, t.TempDir(), strings.Replace(testLab, "metadata: {name: lab}", "metadata: {name: mylab}", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if lf.name != "mylab" {
		t.Errorf("name = %q, want mylab", lf.name)
	}
}

// TestReadLabFileRefuses refuses a lab file its schema does not accept.
func TestReadLabFileRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ lab, want string }{
		"no MAC address":  {strings.Replace(testLab, `cp1: {mac: "52:54:00:7B:00:11"}`, "cp1: {memory: 2048}", 1), "mac"},
		"a malformed MAC": {strings.Replace(testLab, "52:54:00:7B:00:11", "52-54-00-7b-00-11", 1), "52-54-00-7b-00-11"},
		"no nodes":        {strings.SplitAfter(testLab, "  nodes:\n")[0] + "    {}\n", "nodes"},
		"another kind":    {strings.Replace(testLab, "kind: Lab", "kind: Inventory", 1), `"Inventory"`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readLabFile(writeLabFiles(t, t.TempDir(), tc.lab)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("readLabFile = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestTheLabFiles reads the lab file of the e2e tests' main cluster and that of the lab template,
// so neither can drift from its inventory unnoticed.
func TestTheLabFiles(t *testing.T) {
	t.Run("e2e", func(t *testing.T) { checkLabFile(t, "../../test/fixtures/clusters", "chalklab/lab.yaml") })
	t.Run("template", func(t *testing.T) { checkLabFile(t, "../../templates/lab", "chalklab.yaml") })
}

// checkLabFile reads the lab file at labFile below a copy of src, with a kvm image where each of
// its nodes' image in the inventory points, and checks that each node's MAC address is the one its
// Network documents match.
func checkLabFile(t *testing.T, src, labFile string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
		t.Fatal(err)
	}
	lf, err := readLabFile(filepath.Join(dir, labFile))
	if err != nil {
		t.Fatal(err)
	}
	inv, err := inventory.Load(lf.inventory)
	if err != nil {
		t.Fatal(err)
	}
	for name := range lf.nodes {
		nc, err := inv.Node(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(nc.ImageDir, 0o755); err != nil {
			t.Fatal(err)
		}
		md := `{"schemaVersion": 0, "id": "chalkos", "variant": "k8s", "platform": "kvm", "rootHash": "` + strings.Repeat("ab", 32) + `", "kinds": []}`
		if err := os.WriteFile(filepath.Join(nc.ImageDir, "metadata.json"), []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	nodes, _, err := labNodes(inv, lf)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) == 0 {
		t.Fatal("the lab file runs no node")
	}
	for _, n := range nodes {
		nc, err := inv.Node(n.Name)
		if err != nil {
			t.Fatal(err)
		}
		var matched []string
		for _, d := range nc.Documents {
			if d.Kind != "Network" {
				continue
			}
			spec, _ := d.Object["spec"].(map[string]any)
			match, _ := spec["match"].(map[string]any)
			if mac, ok := match["macAddress"].(string); ok {
				matched = append(matched, strings.ToLower(mac))
			}
		}
		if !slices.Contains(matched, n.MAC) {
			t.Errorf("%s gives %s the MAC address %s, but its Network documents match %q", labFile, n.Name, n.MAC, matched)
		}
	}
}

// TestLabInventory writes the inventory chalkctl reaches the lab with: the lab's nodes alone at
// their forwarded ports, and every path absolute.
func TestLabInventory(t *testing.T) {
	dir := t.TempDir()
	lf, err := readLabFile(writeLabFiles(t, dir, testLab))
	if err != nil {
		t.Fatal(err)
	}
	inv, err := inventory.Load(lf.inventory)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "chalkos.yaml")
	l := &lab.Lab{Nodes: []lab.LabNode{{Name: "cp1", ChalkdPort: 15001}}}
	if err := writeLabInventory(path, inv, l); err != nil {
		t.Fatal(err)
	}
	got, err := inventory.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.NodeNames(), []string{"cp1"}) || got.Spec.Nodes["cp1"].Address != "127.0.0.1:15001" ||
		!slices.Equal(got.Spec.Files, []string{filepath.Join(dir, "cluster.yaml")}) {
		t.Errorf("the lab's inventory = %+v", got.Spec)
	}
	nc, err := got.Node("cp1")
	if err != nil || nc.ImageDir != filepath.Join(dir, "images", "node-kvm") {
		t.Errorf("cp1's config = %+v, %v", nc, err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the lab's inventory: %v, %v; want mode 0600", info.Mode(), err)
	}
}

// TestCreateKeys writes the lab's Secure Boot keys for its owner alone, and enrolls their
// certificates in the firmware's variables with Secure Boot on.
func TestCreateKeys(t *testing.T) {
	a, _ := testApp()
	var enrolled []string
	a.output = func(_ context.Context, name string, args ...string) ([]byte, error) {
		enrolled = append([]string{name}, args...)
		return nil, nil
	}
	dir := filepath.Join(t.TempDir(), "keys")
	if err := a.createKeys(context.Background(), dir, "lab", "/fw/VARS.fd"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the keys' directory: %v, %v", info.Mode(), err)
	}
	for _, name := range []string{"PK", "KEK", "db"} {
		info, err := os.Stat(filepath.Join(dir, name+".key"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s.key: %v, %v; want mode 0600", name, info.Mode(), err)
		}
		data, _ := os.ReadFile(filepath.Join(dir, name+".crt"))
		block, _ := pem.Decode(data)
		if block == nil {
			t.Fatalf("%s.crt holds no PEM", name)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || cert.Subject.CommonName != "chalklab lab "+name || cert.NotAfter.Before(time.Now().AddDate(9, 0, 0)) {
			t.Errorf("%s.crt: %v, %v", name, cert.Subject, err)
		}
	}
	args := strings.Join(enrolled, " ")
	for _, want := range []string{"virt-fw-vars --loglevel warning --input /fw/VARS.fd --output " + filepath.Join(dir, "OVMF_VARS.fd"), "--set-pk", "--add-kek", "--add-db", "--secure-boot"} {
		if !strings.Contains(args, want) {
			t.Errorf("enrolled with %q, want %q", args, want)
		}
	}
	if strings.Contains(args, ".key") {
		t.Errorf("a private key was named to virt-fw-vars: %q", args)
	}
	if err := a.createKeys(context.Background(), dir, "lab", "/fw/VARS.fd"); err == nil {
		t.Error("created keys over the lab's keys")
	}
}

// TestCreateLeavesNothingWhenItFails refuses a lab that exists, and removes what a failed create
// made before anything ran. It needs a secrets file. The lab mylab keeps its state below its own
// name, not below that of its inventory, lab.
func TestCreateLeavesNothingWhenItFails(t *testing.T) {
	fakeKVM(t)
	// Short, as the lab's socket paths must be.
	state, err := os.MkdirTemp("", "cl")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(state) })
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("CHALKLAB_OVMF_CODE", "/fw/CODE.fd")
	t.Setenv("CHALKLAB_OVMF_VARS", "/fw/VARS.fd")
	path := writeLabFiles(t, t.TempDir(), strings.Replace(testLab, "metadata: {name: lab}", "metadata: {name: mylab}", 1))
	secrets := filepath.Join(filepath.Dir(path), "secrets.json")
	dir, _ := lab.StateDir("mylab")
	if dir != filepath.Join(state, "chalklab", "mylab") {
		t.Fatalf("StateDir(mylab) = %s", dir)
	}
	a, _ := testApp()
	for _, args := range [][]string{{}, {"--secrets", "-"}, {"--secrets", secrets}} {
		err = a.run(context.Background(), append([]string{"create", "--lab", path}, args...))
		if err == nil || !strings.Contains(err.Error(), "secrets file") {
			t.Errorf("create with %q = %v, want the secrets file asked for", args, err)
		}
	}
	if err := os.WriteFile(secrets, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The test app runs no virt-fw-vars, so creating the lab's keys fails.
	err = a.run(context.Background(), []string{"create", "--lab", path, "--secrets", secrets})
	if err == nil || !strings.Contains(err.Error(), "not in the test") {
		t.Fatalf("create = %v, want the keys' enrolment to fail", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a failed create left %s: %v", dir, err)
	}
	// A lab of the inventory's name is another lab and does not stand in the way.
	if err := os.MkdirAll(filepath.Join(state, "chalklab", "lab"), 0o700); err != nil {
		t.Fatal(err)
	}
	err = a.run(context.Background(), []string{"create", "--lab", path, "--secrets", secrets})
	if err == nil || !strings.Contains(err.Error(), "not in the test") {
		t.Fatalf("create beside the lab lab = %v, want the keys' enrolment to fail", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	err = a.run(context.Background(), []string{"create", "--lab", path, "--secrets", secrets})
	if err == nil || !strings.Contains(err.Error(), "the lab mylab exists in "+dir+"; chalklab destroy removes it") {
		t.Fatalf("create over a lab = %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("create removed the existing lab: %v", err)
	}
}

// TestLabDir takes the only lab there is, and asks to choose among several.
func TestLabDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if _, _, err := labDir(""); err == nil || !strings.Contains(err.Error(), "chalklab create starts one") {
		t.Errorf("labDir without labs = %v", err)
	}
	for _, name := range []string{"lab", "other"} {
		dir, _ := lab.StateDir(name)
		os.MkdirAll(dir, 0o700)
		if err := (&lab.Lab{Name: name}).Write(dir); err != nil {
			t.Fatal(err)
		}
		if name == "lab" {
			if dir, l, err := labDir(""); err != nil || l.Name != "lab" || !strings.HasSuffix(dir, "/chalklab/lab") {
				t.Errorf("labDir with one lab = %q, %v, %v", dir, l, err)
			}
		}
	}
	if _, _, err := labDir(""); err == nil || !strings.Contains(err.Error(), "choose one with --name") {
		t.Errorf("labDir with two labs = %v", err)
	}
	if _, l, err := labDir("other"); err != nil || l.Name != "other" {
		t.Errorf("labDir(other) = %v, %v", l, err)
	}
	if _, _, err := labDir("none"); err == nil {
		t.Error("found a lab that does not exist")
	}
}

// writeLab records a lab of cp1 and w1 that nothing runs, in a new state directory.
func writeLab(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir, _ := lab.StateDir("lab")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	l := &lab.Lab{Name: "lab", Nodes: []lab.LabNode{{Name: "cp1", ChalkdPort: 15001, APIPort: 16443}, {Name: "w1", ChalkdPort: 15002}}}
	if err := l.Write(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestNameChoosesTheLab shows the lab --name names among several, and knows no --cluster.
func TestNameChoosesTheLab(t *testing.T) {
	writeLab(t)
	dir, _ := lab.StateDir("mylab")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := (&lab.Lab{Name: "mylab"}).Write(dir); err != nil {
		t.Fatal(err)
	}
	a, out := testApp()
	if err := a.run(context.Background(), []string{"status", "--name", "mylab"}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "lab mylab in "+dir+";") {
		t.Errorf("status --name mylab = %q", out)
	}
	for _, cmd := range []string{"status", "console", "sign", "destroy", "start"} {
		if err := a.run(context.Background(), []string{cmd, "--cluster", "mylab"}); err == nil || !strings.Contains(err.Error(), "unknown flag: --cluster") {
			t.Errorf("%s --cluster = %v, want the flag unknown", cmd, err)
		}
	}
}

// TestDestroyTwice removes a lab, and has nothing to destroy the second time.
func TestDestroyTwice(t *testing.T) {
	dir := writeLab(t)
	a, out := testApp()
	if err := a.run(context.Background(), []string{"destroy"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destroy left %s: %v", dir, err)
	}
	for _, args := range [][]string{{"destroy"}, {"destroy", "--name", "lab"}} {
		out.Reset()
		if err := a.run(context.Background(), args); err != nil {
			t.Errorf("%v of a lab destroyed = %v", args, err)
		}
		if !strings.Contains(out.String(), "nothing to destroy") {
			t.Errorf("%v printed %q", args, out)
		}
	}
}

// TestStatusOfAStoppedLab shows the VMs that do not run and how to start them again.
func TestStatusOfAStoppedLab(t *testing.T) {
	writeLab(t)
	a, out := testApp()
	if err := a.run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"no supervisor runs it", "cp1 and w1 do not run; chalklab start starts the lab again"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status = %q, want %q", out, want)
		}
	}
}

// TestStartSignalsTheSupervisor asks the supervisor that runs the lab to start its VMs that do
// not run, and refuses a lab that is not and a machine without KVM.
func TestStartSignalsTheSupervisor(t *testing.T) {
	dir := writeLab(t)
	fakeKVM(t)
	lock, err := os.OpenFile(filepath.Join(dir, "supervisor.pid"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	// The supervisor is this process.
	lock.WriteString(strconv.Itoa(os.Getpid()) + "\n")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGUSR1)
	defer signal.Stop(sig)
	a, out := testApp()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.run(ctx, []string{"start"}) }()
	select {
	case <-sig:
	case err := <-done:
		t.Fatalf("start of a running lab = %v, want the supervisor signalled", err)
	case <-time.After(10 * time.Second):
		t.Fatal("start did not signal the supervisor")
	}
	cancel()
	<-done
	if !strings.Contains(out.String(), "starting cp1 and w1") {
		t.Errorf("start printed %q, want the VMs that do not run", out)
	}

	if err := a.run(context.Background(), []string{"start", "--name", "other"}); err == nil || !strings.Contains(err.Error(), "chalklab create") {
		t.Errorf("start of no lab = %v, want chalklab create named", err)
	}
	missing := filepath.Join(t.TempDir(), "kvm")
	kvmDevice = missing
	if err := a.run(context.Background(), []string{"start"}); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("start without KVM = %v, want %s named", err, missing)
	}
}

// fakeKVM stands in for /dev/kvm with a file the test may open.
func fakeKVM(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := kvmDevice
	kvmDevice = path
	t.Cleanup(func() { kvmDevice = old })
}

// TestCreateNeedsKVM refuses to run a lab without access to /dev/kvm, before anything is built.
func TestCreateNeedsKVM(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("CHALKLAB_OVMF_CODE", "/fw/CODE.fd")
	t.Setenv("CHALKLAB_OVMF_VARS", "/fw/VARS.fd")
	fakeKVM(t)
	missing := filepath.Join(t.TempDir(), "kvm")
	kvmDevice = missing
	a, _ := testApp()
	err := a.run(context.Background(), []string{"create", "--lab", "/nonexistent.yaml"})
	if err == nil || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "KVM") {
		t.Errorf("create without KVM = %v, want %s named", err, missing)
	}
	if os.Geteuid() != 0 {
		if err := os.WriteFile(missing, nil, 0o000); err != nil {
			t.Fatal(err)
		}
		if err := a.run(context.Background(), []string{"create", "--lab", "/nonexistent.yaml"}); err == nil || !strings.Contains(err.Error(), "kvm group") {
			t.Errorf("create without access to KVM = %v, want the kvm group named", err)
		}
	}
}

// TestSignIntoAnExistingDirectory copies an image into a directory that exists, again and again.
func TestSignIntoAnExistingDirectory(t *testing.T) {
	writeLab(t)
	img := t.TempDir()
	for name, data := range map[string]string{"chalkos.raw": "raw", "repart-output.json": "[]", "repart.d/10-esp.conf": "[Partition]\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(img, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(img, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := t.TempDir()
	a, _ := testApp()
	for range 2 {
		// The test's image has no ESP to sign.
		err := a.run(context.Background(), []string{"sign", img, "--out", out})
		if err == nil || strings.Contains(err.Error(), "exists") {
			t.Errorf("sign into %s = %v, want only the ESP missing", out, err)
		}
		for _, name := range []string{"chalkos.raw", "repart-output.json", "repart.d/10-esp.conf"} {
			if _, err := os.Stat(filepath.Join(out, name)); err != nil {
				t.Errorf("the copy: %v", err)
			}
		}
	}
}

// TestStatusNamesTheLabsFiles names the kubeconfig, the inventory, the client file and the db key and
// certificate an upgrade's images are signed with.
func TestStatusNamesTheLabsFiles(t *testing.T) {
	dir := writeLab(t)
	for _, name := range []string{kubeconfigFile, inventoryFile, clientFile, "keys/db.key", "keys/db.crt"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a, out := testApp()
	if err := a.run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kubeconfig: " + dir + "/kubeconfig", "inventory: " + dir + "/chalkos.yaml", "client file: " + dir + "/chalkctl.json", "Secure Boot db key: " + dir + "/keys/db.key", "Secure Boot db certificate: " + dir + "/keys/db.crt"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("status = %q, want %q", out, want)
		}
	}
}

// TestWaitBootstrapPrintsTheStatus waits for the control plane to wait for its bootstrap, and
// prints the status line that says so, which names the cluster's endpoint.
func TestWaitBootstrapPrintsTheStatus(t *testing.T) {
	a, out := testApp()
	const line = "kubernetes controlplane: waiting for bootstrap or for the cluster at https://192.168.123.11:6443"
	a.chalkctl = func(_ context.Context, w io.Writer, args ...string) error {
		if args[0] != "status" || args[1] != "cp1" {
			return errors.New("unexpected chalkctl " + strings.Join(args, " "))
		}
		_, err := io.WriteString(w, "config abc (the inventory's)\n"+line+"\n")
		return err
	}
	if err := a.waitBootstrap(context.Background(), "cp1", nil); err != nil {
		t.Fatal(err)
	}
	if want := "cp1: " + line + "\n"; !strings.Contains(out.String(), want) {
		t.Errorf("output %q, want %q", out, want)
	}
}

// run runs chalklab's command tree with args, with a's streams, and returns the error of the
// command that ran.
func (a *app) run(ctx context.Context, args []string) error {
	root := newCommand(a)
	root.SetArgs(args)
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	return root.ExecuteContext(ctx)
}

func TestExecute(t *testing.T) {
	// cp1's console holds a line, so console without --follow prints it and ends.
	dir := writeLab(t)
	console := filepath.Join(dir, "cp1", "console.log")
	if err := os.MkdirAll(filepath.Dir(console), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(console, []byte("cp1 login:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for args, want := range map[string]int{
		"": 2, "bogus": 2, "status --bogus": 1, "status --help": 0, "console": 1,
		"console cp1 -f=false": 0, "console cp1 --follow=false": 0,
		// A flag before a subcommand's name is a usage error, in either form.
		"--bogus": 2, "--name=x status": 2, "--name x status": 2,
	} {
		a, out := testApp()
		var stderr bytes.Buffer
		a.stderr = &stderr
		root := newCommand(a)
		root.SetArgs(strings.Fields(args))
		root.SetOut(out)
		root.SetErr(&stderr)
		if got := Execute(context.Background(), root); got != want {
			t.Errorf("chalklab %s exits with %d, want %d:\n%s", args, got, want, stderr.String())
		}
		if want == 2 && !strings.Contains(stderr.String(), "Usage:") {
			t.Errorf("chalklab %s prints no usage:\n%s", args, stderr.String())
		}
	}
}

// The supervisor create starts runs chalklab's hidden supervise command, also when a program
// runs chalklab's tree under a root of its own.
func TestSuperviseArgsFindTheSupervisor(t *testing.T) {
	a, _ := testApp()
	root := newCommand(a)
	b, _ := testApp()
	embedded := newCommand(b)
	embedded.Use = "lab"
	foo := &cobra.Command{Use: "foo"}
	foo.AddCommand(embedded)
	for _, c := range []struct {
		root *cobra.Command
		args []string
	}{
		{root, a.superviseArgs("/tmp/lab")},
		{foo, b.superviseArgs("/tmp/lab")},
	} {
		cmd, rest, err := c.root.Find(c.args)
		if err != nil || cmd.Name() != superviseName || !cmd.Hidden || !reflect.DeepEqual(rest, []string{"/tmp/lab"}) {
			t.Errorf("%s %v finds %v %v, %v; want the hidden supervise command with the directory", c.root.Name(), c.args, cmd, rest, err)
		}
	}
}

// Every command but the supervisor's is described for its help and the reference pages.
func TestCommandsDescribed(t *testing.T) {
	a, _ := testApp()
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if !cmd.Hidden && (cmd.Short == "" || cmd.Long == "" || cmd.Example == "") {
			t.Errorf("%s lacks a short or long description or examples", cmd.CommandPath())
		}
		for _, c := range cmd.Commands() {
			walk(c)
		}
	}
	walk(newCommand(a))
}

// TestPrintNext names the lab's client file in an export of CHALKOSCONFIG, so the chalkctl
// commands after it name no client file, and the kubeconfig only once a control plane runs.
func TestPrintNext(t *testing.T) {
	a, out := testApp()
	l := &lab.Lab{Name: "lab", Nodes: []lab.LabNode{{Name: "cp1", Kind: "controlplane"}, {Name: "w1", Kind: "worker"}}}
	a.printNext("/state/lab", l, true)
	want := `
the lab lab runs cp1, w1; next:
  export KUBECONFIG=/state/lab/kubeconfig
  export CHALKOSCONFIG=/state/lab/chalkctl.json
  kubectl get nodes
  chalkctl status cp1 --inventory /state/lab/chalkos.yaml
  chalklab status
  chalklab destroy
The lab's inventory names unsigned images, which the lab's firmware refuses: upgrade with the
lab's db key, or with --image of an image chalklab sign signed:
  chalkctl upgrade --inventory /state/lab/chalkos.yaml --allow-downtime \
    --sign-key /state/lab/keys/db.key --sign-cert /state/lab/keys/db.crt
`
	if out.String() != want {
		t.Errorf("output:\n%s\nwant:\n%s", out, want)
	}
	out.Reset()
	a.printNext("/state/lab", l, false)
	if s := out.String(); strings.Contains(s, "KUBECONFIG") || strings.Contains(s, "kubectl") || !strings.Contains(s, "export CHALKOSCONFIG=/state/lab/chalkctl.json\n") {
		t.Errorf("output without Kubernetes:\n%s", s)
	}
}

// TestPrintNextAllowsDowntimeBelowThreeControlPlanes names --allow-downtime in the upgrade it
// prints only for a lab of one or two control planes, whose etcd loses its quorum while one
// reboots.
func TestPrintNextAllowsDowntimeBelowThreeControlPlanes(t *testing.T) {
	for _, tc := range []struct {
		kinds []string
		want  bool
	}{{[]string{"controlplane", "worker"}, true}, {[]string{"controlplane", "controlplane"}, true}, {[]string{"controlplane", "controlplane", "controlplane"}, false}, {[]string{""}, false}} {
		a, out := testApp()
		l := &lab.Lab{Name: "lab"}
		for i, k := range tc.kinds {
			l.Nodes = append(l.Nodes, lab.LabNode{Name: "n" + strconv.Itoa(i), Kind: k})
		}
		a.printNext("/state/lab", l, false)
		if got := strings.Contains(out.String(), "--allow-downtime"); got != tc.want {
			t.Errorf("%v: --allow-downtime %v, want %v:\n%s", tc.kinds, got, tc.want, out)
		}
	}
}
