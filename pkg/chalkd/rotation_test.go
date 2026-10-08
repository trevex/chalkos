package chalkd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

func rotationStep(ctx context.Context, s *Server, step nodev1.RotationStep) (*nodev1.RotationStepResponse, error) {
	resp, err := s.RotationStep(ctx, connect.NewRequest(&nodev1.RotationStepRequest{Step: step}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// withAPI makes the node reach a fake API server holding the objects.
func withAPI(s *Server, objects ...runtimeObject) *fake.Clientset {
	client := fake.NewClientset(objects...)
	s.Kubernetes.APIClient = func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) { return client, nil }
	s.Kubernetes.RotationPoll = 10 * time.Millisecond
	return client
}

func counts(encrypted []*nodev1.EncryptedObjects) map[string]uint64 {
	m := map[string]uint64{}
	for _, e := range encrypted {
		m[e.Resource+"/"+e.Key] = e.Objects
	}
	return m
}

// TestCountEncrypted checks the count of the objects etcd holds under each key against an etcd
// member, over more objects than one page.
func TestCountEncrypted(t *testing.T) {
	s, cli, _ := memberServer(t)
	ctx := context.Background()
	for i := range 450 {
		if _, err := cli.Put(ctx, fmt.Sprintf("/registry/secrets/default/old-%03d", i), "k8s:enc:secretbox:v1:chalkos:\x00ciphertext"); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{
		"/registry/secrets/kube-system/new":  "k8s:enc:secretbox:v1:chalkos-20261009t120000z:ciphertext:with:colons",
		"/registry/secrets/default/plain":    "k8s\x00\n\x0fv1\x12\x06Secret",
		"/registry/configmaps/default/other": "k8s:enc:secretbox:v1:chalkos:not-a-secret",
		"/registry/secretsx/default/other":   "k8s:enc:secretbox:v1:chalkos:not-a-secret",
	} {
		if _, err := cli.Put(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	resp, err := rotationStep(ctx, s, nodev1.RotationStep_ROTATION_STEP_COUNT_ENCRYPTED)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]uint64{"secrets/chalkos": 450, "secrets/chalkos-20261009t120000z": 1, "secrets/": 1}
	if got := counts(resp.Encrypted); !mapsEqual(got, want) {
		t.Errorf("counts %v, want %v", got, want)
	}
}

func mapsEqual(a, b map[string]uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func TestEncryptionKeyOf(t *testing.T) {
	for value, want := range map[string]string{
		"k8s:enc:secretbox:v1:chalkos:data":    "chalkos",
		"k8s:enc:secretbox:v1:chalkos-2:a:b:c": "chalkos-2",
		"k8s\x00protobuf":                      "",
		`{"kind":"Secret"}`:                    "",
		"k8s:enc:short":                        "",
	} {
		if got := EncryptionKeyOf([]byte(value)); got != want {
			t.Errorf("%q: %q, want %q", value, got, want)
		}
	}
}

// TestRewriteEncrypted checks that every Secret is updated unchanged and the counts follow.
func TestRewriteEncrypted(t *testing.T) {
	s, _, _ := memberServer(t)
	var objects []runtimeObject
	for i := range 3 {
		objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: fmt.Sprintf("s%d", i)}, Data: map[string][]byte{"k": []byte("v")}})
	}
	client := withAPI(s, objects...)
	resp, err := rotationStep(context.Background(), s, nodev1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rewritten != 3 {
		t.Errorf("rewrote %d secrets, want 3", resp.Rewritten)
	}
	updates := 0
	for _, a := range client.Actions() {
		if a.GetVerb() == "update" && a.GetResource().Resource == "secrets" {
			updates++
		}
	}
	if updates != 3 {
		t.Errorf("%d updates, want one of each secret", updates)
	}
	for _, name := range []string{"s0", "s1", "s2"} {
		got, err := client.CoreV1().Secrets("ns").Get(context.Background(), name, metav1.GetOptions{})
		if err != nil || string(got.Data["k"]) != "v" {
			t.Errorf("%s changed: %v", name, err)
		}
	}
}

func TestListTokenSecrets(t *testing.T) {
	s, _, _ := memberServer(t)
	withAPI(s,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "b", Name: "legacy"}, Type: corev1.SecretTypeServiceAccountToken},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "legacy"}, Type: corev1.SecretTypeServiceAccountToken},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "opaque"}, Type: corev1.SecretTypeOpaque},
	)
	resp, err := rotationStep(context.Background(), s, nodev1.RotationStep_ROTATION_STEP_LIST_TOKEN_SECRETS)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.TokenSecrets, []string{"a/legacy", "b/legacy"}) {
		t.Errorf("token secrets %v", resp.TokenSecrets)
	}
}

// TestRestartAddons checks that the workloads of the cluster's manifests restart once
// kube-root-ca.crt holds every Kubernetes CA, and that the step waits until they rolled out.
func TestRestartAddons(t *testing.T) {
	s, _, _ := memberServer(t)
	share, err := knode.ReadShare(s.Kubernetes.Paths)
	if err != nil {
		t.Fatal(err)
	}
	write(t, s.Kubernetes.Manifests, `[
	  {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "coredns", "namespace": "kube-system"}},
	  {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "coredns", "namespace": "kube-system"}},
	  {"apiVersion": "apps/v1", "kind": "DaemonSet", "metadata": {"name": "kube-flannel-ds", "namespace": "kube-flannel"}}
	]`)
	two := int32(2)
	other := secretsCA(t)
	client := withAPI(s,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "kube-root-ca.crt"}, Data: map[string]string{"ca.crt": share.CABundle()}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-flannel", Name: "kube-root-ca.crt"}, Data: map[string]string{"ca.crt": other}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "coredns"}, Spec: appsv1.DeploymentSpec{Replicas: &two},
			Status: appsv1.DeploymentStatus{Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-flannel", Name: "kube-flannel-ds"},
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 2, UpdatedNumberScheduled: 1, NumberAvailable: 2}},
	)
	ctx := context.Background()

	// kube-flannel's kube-root-ca.crt holds another CA: nothing restarts.
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if _, err := rotationStep(short, s, nodev1.RotationStep_ROTATION_STEP_RESTART_ADDONS); err == nil || !strings.Contains(err.Error(), "kube-root-ca.crt of kube-flannel") {
		t.Fatalf("err = %v, want a wait for kube-flannel's kube-root-ca.crt", err)
	}
	if d, _ := client.AppsV1().Deployments("kube-system").Get(ctx, "coredns", metav1.GetOptions{}); d.Spec.Template.Annotations[RestartedAnnotation] != "" {
		t.Error("coredns restarted before every namespace trusted the CAs")
	}

	// Once it holds the CAs, the workloads restart, and the step waits for flannel's rollout.
	cm, _ := client.CoreV1().ConfigMaps("kube-flannel").Get(ctx, "kube-root-ca.crt", metav1.GetOptions{})
	cm.Data["ca.crt"] = share.CABundle()
	if _, err := client.CoreV1().ConfigMaps("kube-flannel").Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		ds, _ := client.AppsV1().DaemonSets("kube-flannel").Get(ctx, "kube-flannel-ds", metav1.GetOptions{})
		ds.Status.UpdatedNumberScheduled = 2
		client.AppsV1().DaemonSets("kube-flannel").UpdateStatus(ctx, ds, metav1.UpdateOptions{})
	}()
	resp, err := rotationStep(ctx, s, nodev1.RotationStep_ROTATION_STEP_RESTART_ADDONS)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(resp.Restarted, []string{"kube-system/Deployment/coredns", "kube-flannel/DaemonSet/kube-flannel-ds"}) {
		t.Errorf("restarted %v", resp.Restarted)
	}
	d, _ := client.AppsV1().Deployments("kube-system").Get(ctx, "coredns", metav1.GetOptions{})
	ds, _ := client.AppsV1().DaemonSets("kube-flannel").Get(ctx, "kube-flannel-ds", metav1.GetOptions{})
	if d.Spec.Template.Annotations[RestartedAnnotation] == "" || ds.Spec.Template.Annotations[RestartedAnnotation] == "" {
		t.Error("a workload's pod template was not changed")
	}
	if ds.Status.UpdatedNumberScheduled != 2 {
		t.Error("the step ended before flannel rolled out")
	}
}

// secretsCA is the certificate of a CA of other secrets.
func secretsCA(t *testing.T) string {
	t.Helper()
	share, err := kpki.ParseShare(testShare(t, k8s.KindControlPlane))
	if err != nil {
		t.Fatal(err)
	}
	return share.CA.Certificate
}

// TestRenewKubeletServing checks that the kubelet's serving certificates are removed and the
// kubelet restarted, on workers too.
func TestRenewKubeletServing(t *testing.T) {
	s, r := kubernetesServer(t, k8s.KindWorker, true)
	p := s.Kubernetes.Paths
	for _, name := range []string{"kubelet-server-2026-10-08.pem", "kubelet-client-current.pem"} {
		write(t, filepath.Join(p.KubeletPKI, name), "pem")
	}
	if err := os.Symlink("kubelet-server-2026-10-08.pem", p.KubeletServing()); err != nil {
		t.Fatal(err)
	}
	if _, err := rotationStep(context.Background(), s, nodev1.RotationStep_ROTATION_STEP_RENEW_KUBELET_SERVING); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(p.KubeletPKI, "kubelet-server-*")); len(left) != 0 {
		t.Errorf("left %v", left)
	}
	if _, err := os.Stat(filepath.Join(p.KubeletPKI, "kubelet-client-current.pem")); err != nil {
		t.Error("the client certificate was removed")
	}
	if !slices.Contains(r.calls, "systemctl restart kubelet.service") {
		t.Errorf("calls %v, want the kubelet restarted", r.calls)
	}
	// A worker runs none of the control plane's steps.
	if _, err := rotationStep(context.Background(), s, nodev1.RotationStep_ROTATION_STEP_COUNT_ENCRYPTED); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a worker counted encrypted objects: %v", err)
	}
}

// runtimeObject is an object the fake API server holds.
type runtimeObject = runtime.Object
