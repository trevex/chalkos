package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// rotationSecret is the Secret whose data must survive every rotation, the encryption key's
// above all.
var rotationSecret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "rotation"}, Data: map[string][]byte{"value": []byte("kept")}}

// rotations rotates the OS CA, the Kubernetes CAs, the service-account key and the encryption
// key one after the other. After each finish the pods reach each other and resolve names, the
// Secret reads back, and what the old value issued is refused: a client file of the old OS CA,
// a kubeconfig of the old Kubernetes CA. The rotations update a copy of the test's secrets file,
// which every later chalkctl call reads.
func rotations(t *testing.T, ctx context.Context, nodes map[string]*node, p peers, dir string, apiPort int) {
	t.Helper()
	secrets := copySecrets(t, dir)
	t.Setenv("CHALKLAB_SECRETS", secrets)
	kubeconfig := func(name string) (string, kubernetes.Interface) {
		path := filepath.Join(dir, name)
		if _, err := chalkctl(t, nil, "base", "kubeconfig", "--out", path, "--force", "--server", fmt.Sprintf("https://127.0.0.1:%d", apiPort)); err != nil {
			t.Fatal(err)
		}
		cfg, err := clientcmd.BuildConfigFromFlags("", path)
		if err != nil {
			t.Fatal(err)
		}
		cs, err := kubernetes.NewForConfig(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return path, cs
	}
	oldKubeconfig, cs := kubeconfig("kubeconfig-before-rotations")
	if _, err := cs.CoreV1().Secrets("default").Create(ctx, rotationSecret, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	// Every node of the cluster definition takes part in a rotation; this test runs cp1 and w1
	// alone of the nodes base.json defines.
	manifest := clusterOf(t, dir, "cp1", "w1")
	rotate := func(args ...string) {
		t.Helper()
		args = append([]string{"rotate"}, args...)
		args = append(args, "--manifest", manifest, "--secrets", secrets)
		for _, name := range []string{"cp1", "w1"} {
			args = append(args, "--endpoint", name+"="+nodes[name].addr)
		}
		// A phase waits for the control plane's pods to restart, which takes minutes on a loaded
		// machine.
		rctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(rctx, os.Getenv("CHALKLAB_CHALKCTL"), args...).CombinedOutput()
		t.Logf("chalkctl %s:\n%s", strings.Join(args, " "), out)
		if err != nil {
			t.Fatalf("chalkctl %s: %v", strings.Join(args, " "), err)
		}
	}
	// The cluster keeps working after each finish.
	works := func(cs kubernetes.Interface, what string, since time.Time) {
		t.Helper()
		seen := metav1.NewTime(since)
		waitFor(t, 10*time.Minute, "pods reaching each other and resolving names after "+what, func() error { return p.reached(ctx, cs, &seen) })
		got, err := cs.CoreV1().Secrets("default").Get(ctx, rotationSecret.Name, metav1.GetOptions{})
		if err != nil || string(got.Data["value"]) != "kept" {
			t.Errorf("the Secret after %s: %v", what, err)
		}
		waitFor(t, 5*time.Minute, "both nodes Ready after "+what, func() error { return nodesReady(ctx, cs, "cp1", "w1") })
	}
	timed := func(what string, rotation func()) {
		t.Helper()
		start := time.Now()
		rotation()
		t.Logf("%s took %v", what, time.Since(start).Round(time.Second))
	}

	oldReader := filepath.Join(dir, "reader.json")
	timed("the OS CA's rotation", func() {
		start := time.Now()
		rotate("os-ca")
		rotate("os-ca", "--finish")
		if out, err := chalkctlWith(t, nodes["w1"], "base", oldReader, "status", "w1"); err == nil {
			t.Errorf("a client file of the old OS CA reached w1 after the finish:\n%s", out)
		}
		newReader := filepath.Join(dir, "reader-after-rotation.json")
		if _, err := chalkctl(t, nil, "base", "config", "new", "--name", "e2e-reader", "--role", "reader", "--out", newReader); err != nil {
			t.Fatal(err)
		}
		if _, err := chalkctlWith(t, nodes["w1"], "base", newReader, "status", "w1"); err != nil {
			t.Errorf("a client file of the new OS CA: %v", err)
		}
		works(cs, "the OS CA's rotation", start)
	})

	timed("the Kubernetes CAs' rotation", func() {
		start := time.Now()
		rotate("kubernetes-ca")
		rotate("kubernetes-ca", "--resume")
		_, cs = kubeconfig("kubeconfig-after-rotation")
		// The old kubeconfig cannot verify the API server any more; its certificate still
		// authenticates until the finish.
		if err := authenticates(ctx, oldKubeconfig); err != nil {
			t.Errorf("the certificate of the old kubeconfig before the finish: %v", err)
		}
		rotate("kubernetes-ca", "--finish")
		if err := authenticates(ctx, oldKubeconfig); !apierrors.IsUnauthorized(err) {
			t.Errorf("the certificate of the old kubeconfig after the finish: %v, want unauthorized", err)
		}
		works(cs, "the Kubernetes CAs' rotation", start)
	})

	timed("the service-account key's rotation", func() {
		start := time.Now()
		rotate("service-account-key")
		// The test does not wait the hour in which kubelets renew the tokens of other pods.
		rotate("service-account-key", "--finish", "--force")
		works(cs, "the service-account key's rotation", start)
	})

	timed("the encryption key's rotation", func() {
		start := time.Now()
		rotate("encryption-key")
		rotate("encryption-key", "--finish")
		works(cs, "the encryption key's rotation", start)
	})
	logMemory(t, ctx, cs)
}

// clusterOf writes the cluster definition of base.json with the named nodes alone, and returns
// its path.
func clusterOf(t *testing.T, dir string, names ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("CHALKLAB_MANIFESTS"), "base.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	all, _ := m["nodes"].(map[string]any)
	nodes := map[string]any{}
	for _, name := range names {
		if all[name] == nil {
			t.Fatalf("base.json has no node %s", name)
		}
		nodes[name] = all[name]
	}
	m["nodes"] = nodes
	if data, err = json.Marshal(m); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "cluster.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// copySecrets copies the test's secrets file and its public half to a directory of the test,
// where chalkctl rotate can update them.
func copySecrets(t *testing.T, dir string) string {
	t.Helper()
	src := os.Getenv("CHALKLAB_SECRETS")
	to := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(to, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Base(src), "secrets.pub.json"} {
		data, err := os.ReadFile(filepath.Join(filepath.Dir(src), name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(to, filepath.Base(src))
}

// authenticates asks the API server for its version with the client certificate of a
// kubeconfig, without verifying the server, whose CA the kubeconfig may no longer hold.
func authenticates(ctx context.Context, kubeconfig string) error {
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return err
	}
	cfg.TLSClientConfig = rest.TLSClientConfig{Insecure: true, CertData: cfg.CertData, KeyData: cfg.KeyData}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	_, err = cs.Discovery().RESTClient().Get().AbsPath("/version").DoRaw(ctx)
	if err == nil {
		return nil
	}
	var status apierrors.APIStatus
	if errors.As(err, &status) {
		return err
	}
	return fmt.Errorf("no answer from the API server: %w", err)
}
