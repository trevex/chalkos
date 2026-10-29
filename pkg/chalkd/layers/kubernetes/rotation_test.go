package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layers/kubernetes/kubernetestest"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	knode "github.com/trevex/chalkos/pkg/kubernetes/node"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

func rotationStep(ctx context.Context, s *Plugin, step kubernetesv1.RotationStep) (*kubernetesv1.RotationStepResponse, error) {
	resp, err := s.RotationStep(ctx, connect.NewRequest(&kubernetesv1.RotationStepRequest{Step: step}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// withAPI makes the node reach a fake API server holding the objects.
func withAPI(s *Plugin, objects ...runtimeObject) *fake.Clientset {
	client := fake.NewClientset(objects...)
	s.k.hooks.APIClient = func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) { return client, nil }
	s.k.timing.Poll = 10 * time.Millisecond
	return client
}

func counts(encrypted []*kubernetesv1.EncryptedObjects) map[string]uint64 {
	m := map[string]uint64{}
	for _, e := range encrypted {
		m[e.Resource+"/"+e.Key] = e.Objects
	}
	return m
}

// TestCountEncrypted checks the count of the objects etcd holds under each key against an etcd
// member, over more objects than one page.
func TestCountEncrypted(t *testing.T) {
	s, cli, _ := memberNode(t)
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
	resp, err := rotationStep(ctx, s, kubernetesv1.RotationStep_ROTATION_STEP_COUNT_ENCRYPTED)
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

// TestRewriteEncrypted checks that every Secret is updated unchanged and the counts follow.
func TestRewriteEncrypted(t *testing.T) {
	s, _, _ := memberNode(t)
	var objects []runtimeObject
	for i := range 3 {
		objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: fmt.Sprintf("s%d", i)}, Data: map[string][]byte{"k": []byte("v")}})
	}
	client := withAPI(s, objects...)
	resp, err := rotationStep(context.Background(), s, kubernetesv1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED)
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
	s, _, _ := memberNode(t)
	withAPI(s,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "b", Name: "legacy"}, Type: corev1.SecretTypeServiceAccountToken},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "legacy"}, Type: corev1.SecretTypeServiceAccountToken},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "opaque"}, Type: corev1.SecretTypeOpaque},
	)
	resp, err := rotationStep(context.Background(), s, kubernetesv1.RotationStep_ROTATION_STEP_LIST_TOKEN_SECRETS)
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
	s, _, _ := memberNode(t)
	share, err := knode.ReadShare(s.k.paths)
	if err != nil {
		t.Fatal(err)
	}
	s.k.hooks.Objects = objectsOf(t, `[
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
	if _, err := rotationStep(short, s, kubernetesv1.RotationStep_ROTATION_STEP_RESTART_ADDONS); err == nil || !strings.Contains(err.Error(), "kube-root-ca.crt of kube-flannel") {
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
	resp, err := rotationStep(ctx, s, kubernetesv1.RotationStep_ROTATION_STEP_RESTART_ADDONS)
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
	share, err := kpki.ParseShare(kubernetestest.Share(t, k8s.KindControlPlane, "n1"))
	if err != nil {
		t.Fatal(err)
	}
	return share.CA.Certificate
}

// TestRenewKubeletServing checks that the kubelet's serving certificates are removed and the
// kubelet restarted, on workers too.
func TestRenewKubeletServing(t *testing.T) {
	s, r := kubernetesNode(t, k8s.KindWorker, true)
	p := s.k.paths
	for _, name := range []string{"kubelet-server-2026-10-08.pem", "kubelet-client-current.pem"} {
		write(t, filepath.Join(p.KubeletPKI, name), "pem")
	}
	if err := os.Symlink("kubelet-server-2026-10-08.pem", p.KubeletServing()); err != nil {
		t.Fatal(err)
	}
	if _, err := rotationStep(context.Background(), s, kubernetesv1.RotationStep_ROTATION_STEP_RENEW_KUBELET_SERVING); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(p.KubeletPKI, "kubelet-server-*")); len(left) != 0 {
		t.Errorf("left %v", left)
	}
	if _, err := os.Stat(filepath.Join(p.KubeletPKI, "kubelet-client-current.pem")); err != nil {
		t.Error("the client certificate was removed")
	}
	if !slices.Contains(r.Calls, "systemctl restart kubelet.service") {
		t.Errorf("calls %v, want the kubelet restarted", r.Calls)
	}
	// A worker runs none of the control plane's steps.
	if _, err := rotationStep(context.Background(), s, kubernetesv1.RotationStep_ROTATION_STEP_COUNT_ENCRYPTED); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a worker counted encrypted objects: %v", err)
	}
}

// runtimeObject is an object the fake API server holds.
type runtimeObject = runtime.Object

// TestRewriteEncryptedRetriesAndSkips checks that a Secret changed since it was listed is read
// again and rewritten, and that one deleted since is skipped.
func TestRewriteEncryptedRetriesAndSkips(t *testing.T) {
	s, _, _ := memberNode(t)
	var objects []runtimeObject
	for i := range 3 {
		objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: fmt.Sprintf("s%d", i)}, Data: map[string][]byte{"k": []byte("v")}})
	}
	client := withAPI(s, objects...)
	conflicted := false
	client.PrependReactor("update", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		secret := action.(k8stesting.UpdateAction).GetObject().(*corev1.Secret)
		switch {
		case secret.Name == "s1" && !conflicted:
			conflicted = true
			return true, nil, apierrors.NewConflict(corev1.Resource("secrets"), "s1", errors.New("the object has been modified"))
		case secret.Name == "s2":
			return true, nil, apierrors.NewNotFound(corev1.Resource("secrets"), "s2")
		}
		return false, nil, nil
	})
	resp, err := rotationStep(context.Background(), s, kubernetesv1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED)
	if err != nil {
		t.Fatal(err)
	}
	if !conflicted || resp.Rewritten != 2 {
		t.Errorf("rewrote %d secrets after a conflict on s1 and s2 deleted, want 2", resp.Rewritten)
	}
	gets := 0
	for _, a := range client.Actions() {
		if a.GetVerb() == "get" && a.GetResource().Resource == "secrets" {
			gets++
		}
	}
	if gets != 1 {
		t.Errorf("%d reads of a Secret, want s1 read again after its conflict", gets)
	}
}

// TestRewriteEncryptedPages checks that the rewrite lists Secrets in pages, and starts over when
// the list it pages through expired, rewriting each Secret once.
func TestRewriteEncryptedPages(t *testing.T) {
	s, _, _ := memberNode(t)
	var objects []runtimeObject
	var all []corev1.Secret
	for i := range 5 {
		secret := corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: fmt.Sprintf("s%d", i)}}
		all = append(all, secret)
		objects = append(objects, &secret)
	}
	client := withAPI(s, objects...)
	expired, lists := false, 0
	client.PrependReactor("list", "secrets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		lists++
		opts := action.(k8stesting.ListActionImpl).ListOptions
		if opts.Limit <= 0 {
			t.Errorf("listed without a limit")
		}
		from, _ := strconv.Atoi(opts.Continue)
		if from > 0 && !expired {
			expired = true
			return true, nil, apierrors.NewResourceExpired("the list expired")
		}
		to := min(from+2, len(all))
		list := &corev1.SecretList{Items: slices.Clone(all[from:to])}
		if to < len(all) {
			list.Continue = strconv.Itoa(to)
		}
		return true, list, nil
	})
	resp, err := rotationStep(context.Background(), s, kubernetesv1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED)
	if err != nil {
		t.Fatal(err)
	}
	if !expired || lists != 5 {
		t.Errorf("%d lists, want the first page, an expired second, and the three pages again", lists)
	}
	updated := map[string]int{}
	for _, a := range client.Actions() {
		if a.GetVerb() == "update" && a.GetResource().Resource == "secrets" {
			updated[a.(k8stesting.UpdateAction).GetObject().(*corev1.Secret).Name]++
		}
	}
	if resp.Rewritten != 5 || len(updated) != 5 {
		t.Errorf("rewrote %d secrets, updated %v, want each of the 5 once", resp.Rewritten, updated)
	}
	for name, n := range updated {
		if n != 1 {
			t.Errorf("%s updated %d times", name, n)
		}
	}
}

// TestRewriteEncryptedErrorsHoldNoData checks that a failed rewrite names the Secret and why,
// never what the API server echoed of it.
func TestRewriteEncryptedErrorsHoldNoData(t *testing.T) {
	s, _, _ := memberNode(t)
	client := withAPI(s, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "s0"}, Data: map[string][]byte{"k": []byte("hunter2")}})
	fail := apierrors.NewGenericServerResponse(500, "update", corev1.Resource("secrets"), "s0", "data.k: hunter2", 0, true)
	fail.ErrStatus.Reason = ""
	client.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, fail })
	_, err := rotationStep(context.Background(), s, kubernetesv1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED)
	if err == nil || !strings.Contains(err.Error(), "ns/s0") || !strings.Contains(err.Error(), "500") || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("err = %v, want one naming ns/s0 and the HTTP status alone", err)
	}
	client.PrependReactor("update", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection reset by peer")
	})
	_, err = rotationStep(context.Background(), s, kubernetesv1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED)
	if err == nil || !strings.Contains(err.Error(), "connection reset by peer") {
		t.Errorf("err = %v, want one saying why", err)
	}
}

// TestUnknownRotationStepMintsNothing checks that an unknown step is refused before chalkd
// creates a credential for the API server.
func TestUnknownRotationStepMintsNothing(t *testing.T) {
	s, _, _ := memberNode(t)
	minted := false
	s.k.hooks.APIClient = func(k8s.Cluster, kpki.Share) (kubernetes.Interface, error) {
		minted = true
		return fake.NewClientset(), nil
	}
	if _, err := rotationStep(context.Background(), s, kubernetesv1.RotationStep(99)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("err = %v, want invalid_argument", err)
	}
	if minted {
		t.Error("an unknown step got a credential for the API server")
	}
}

// TestRestartAddonsDoesNotWaitForWhatNeverRollsOut checks that workloads whose pods a restart does
// not replace are restarted and reported, but not waited for.
func TestRestartAddonsDoesNotWaitForWhatNeverRollsOut(t *testing.T) {
	s, _, _ := memberNode(t)
	share, err := knode.ReadShare(s.k.paths)
	if err != nil {
		t.Fatal(err)
	}
	s.k.hooks.Objects = objectsOf(t, `[
	  {"apiVersion": "apps/v1", "kind": "DaemonSet", "metadata": {"name": "on-delete", "namespace": "apps"}},
	  {"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": {"name": "on-delete", "namespace": "apps"}},
	  {"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": {"name": "partitioned", "namespace": "apps"}},
	  {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "paused", "namespace": "apps"}},
	  {"apiVersion": "apps/v1", "kind": "Deployment", "metadata": {"name": "rolls", "namespace": "apps"}}
	]`)
	one, two := int32(1), int32(2)
	withAPI(s,
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "kube-root-ca.crt"}, Data: map[string]string{"ca.crt": share.CABundle()}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "on-delete"},
			Spec:   appsv1.DaemonSetSpec{UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.OnDeleteDaemonSetStrategyType}},
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 2, NumberAvailable: 2}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "on-delete"},
			Spec:   appsv1.StatefulSetSpec{Replicas: &two, UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}},
			Status: appsv1.StatefulSetStatus{ReadyReplicas: 2}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "partitioned"},
			Spec: appsv1.StatefulSetSpec{Replicas: &two, UpdateStrategy: appsv1.StatefulSetUpdateStrategy{Type: appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{Partition: &one}}},
			Status: appsv1.StatefulSetStatus{ReadyReplicas: 2}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "paused"}, Spec: appsv1.DeploymentSpec{Replicas: &two, Paused: true},
			Status: appsv1.DeploymentStatus{Replicas: 2, AvailableReplicas: 2}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "rolls"}, Spec: appsv1.DeploymentSpec{Replicas: &two},
			Status: appsv1.DeploymentStatus{Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2}},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := rotationStep(ctx, s, kubernetesv1.RotationStep_ROTATION_STEP_RESTART_ADDONS)
	if err != nil {
		t.Fatalf("the step waited for workloads that never roll out on a restart: %v", err)
	}
	if len(resp.Restarted) != 5 {
		t.Errorf("restarted %v, want every workload", resp.Restarted)
	}
	want := []string{"apps/DaemonSet/on-delete", "apps/StatefulSet/on-delete", "apps/StatefulSet/partitioned", "apps/Deployment/paused"}
	if !slices.Equal(resp.NotWaited, want) {
		t.Errorf("not waited for %v, want %v", resp.NotWaited, want)
	}
}

// objectsOf is an Objects hook that returns the objects of the JSON list.
func objectsOf(t *testing.T, list string) func(k8s.Cluster) ([]map[string]any, error) {
	t.Helper()
	var objects []map[string]any
	if err := json.Unmarshal([]byte(list), &objects); err != nil {
		t.Fatal(err)
	}
	return func(k8s.Cluster) ([]map[string]any, error) { return objects, nil }
}
