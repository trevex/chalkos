package chalkd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"connectrpc.com/connect"
	clientv3 "go.etcd.io/etcd/client/v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	kapply "github.com/trevex/chalkos/pkg/kubernetes/apply"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// RestartedAnnotation is the annotation of a workload's pod template that restarting it for a
// rotation sets, as kubectl rollout restart sets its own.
const RestartedAnnotation = "chalkos.dev/restarted-at"

func (s *Server) RotationStep(ctx context.Context, req *connect.Request[nodev1.RotationStepRequest]) (*connect.Response[nodev1.RotationStepResponse], error) {
	k := s.Kubernetes
	if k == nil {
		return nil, failed(connect.CodeFailedPrecondition, "the node's role has no Kubernetes")
	}
	resp := &nodev1.RotationStepResponse{}
	step := req.Msg.Step
	if step == nodev1.RotationStep_ROTATION_STEP_RENEW_KUBELET_SERVING {
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := k.renewKubeletServing(ctx, s); err != nil {
			return nil, err
		}
		return connect.NewResponse(resp), nil
	}
	c, _, share, err := k.member()
	if err != nil {
		return nil, err
	}
	if step == nodev1.RotationStep_ROTATION_STEP_COUNT_ENCRYPTED {
		if resp.Encrypted, err = k.countEncrypted(ctx, c, share); err != nil {
			return nil, failed(connect.CodeFailedPrecondition, "%v", err)
		}
		return connect.NewResponse(resp), nil
	}
	newClient := k.APIClient
	if newClient == nil {
		newClient = apiClient
	}
	client, err := newClient(c, share)
	if err != nil {
		return nil, failed(connect.CodeInternal, "reach the API server: %v", err)
	}
	switch step {
	case nodev1.RotationStep_ROTATION_STEP_RESTART_ADDONS:
		resp.Restarted, err = k.restartAddons(ctx, client, share.CABundle())
	case nodev1.RotationStep_ROTATION_STEP_LIST_TOKEN_SECRETS:
		resp.TokenSecrets, err = tokenSecrets(ctx, client)
	case nodev1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED:
		if resp.Rewritten, err = rewriteEncrypted(ctx, client); err == nil {
			resp.Encrypted, err = k.countEncrypted(ctx, c, share)
		}
	default:
		return nil, failed(connect.CodeInvalidArgument, "unknown rotation step %v", step)
	}
	if err != nil {
		var ce *connect.Error
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, failed(connect.CodeFailedPrecondition, "%v", err)
	}
	return connect.NewResponse(resp), nil
}

// apiClient reaches the node's own API server with chalkd's credential.
func apiClient(c k8s.Cluster, share kpki.Share) (kubernetes.Interface, error) {
	cfg, err := newCredential(share, credentialValidity, time.Now).restConfig(c.LocalAPIServer())
	if err != nil {
		return nil, err
	}
	// Rewriting every encrypted object takes longer than one request.
	cfg.Timeout = time.Minute
	return kubernetes.NewForConfig(cfg)
}

// poll is the time between two looks at what a rotation step waits for.
func (k *Kubernetes) poll() time.Duration {
	if k.RotationPoll > 0 {
		return k.RotationPoll
	}
	return 2 * time.Second
}

// workload is a Deployment, DaemonSet or StatefulSet of the cluster's manifests.
type workload struct{ kind, namespace, name string }

func (w workload) String() string { return w.namespace + "/" + w.kind + "/" + w.name }

// restartAddons restarts the workloads of the cluster's manifests, chalkos's addons among them,
// so their pods trust the Kubernetes CAs of bundle: a pod reads its service account's CA
// certificates when it starts. They restart only once kube-root-ca.crt holds all of them in
// their namespaces, and the step ends once they rolled out.
func (k *Kubernetes) restartAddons(ctx context.Context, client kubernetes.Interface, bundle string) ([]string, error) {
	objects, err := kapply.ReadManifests(k.Manifests)
	if err != nil {
		return nil, err
	}
	var workloads []workload
	for _, o := range objects {
		kind, _ := o["kind"].(string)
		if o["apiVersion"] != "apps/v1" || !slices.Contains([]string{"Deployment", "DaemonSet", "StatefulSet"}, kind) {
			continue
		}
		meta, _ := o["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		namespace, _ := meta["namespace"].(string)
		if namespace == "" {
			namespace = metav1.NamespaceDefault
		}
		workloads = append(workloads, workload{kind, namespace, name})
	}
	want, err := pki.Fingerprints(bundle)
	if err != nil {
		return nil, err
	}
	slices.Sort(want)
	waited := map[string]bool{}
	for _, w := range workloads {
		if waited[w.namespace] {
			continue
		}
		waited[w.namespace] = true
		if err := k.waitFor(ctx, "kube-root-ca.crt of "+w.namespace+" to hold the Kubernetes CAs", func() error {
			return rootCAHolds(ctx, client, w.namespace, want)
		}); err != nil {
			return nil, err
		}
	}
	patch := fmt.Appendf(nil, `{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`, RestartedAnnotation, time.Now().UTC().Format(time.RFC3339))
	var restarted []string
	for _, w := range workloads {
		apps := client.AppsV1()
		var err error
		switch w.kind {
		case "Deployment":
			_, err = apps.Deployments(w.namespace).Patch(ctx, w.name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		case "DaemonSet":
			_, err = apps.DaemonSets(w.namespace).Patch(ctx, w.name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		case "StatefulSet":
			_, err = apps.StatefulSets(w.namespace).Patch(ctx, w.name, types.StrategicMergePatchType, patch, metav1.PatchOptions{})
		}
		if err != nil {
			return restarted, fmt.Errorf("restart %s: %w", w, err)
		}
		log.Printf("rotation: restarted %s", w)
		restarted = append(restarted, w.String())
	}
	for _, w := range workloads {
		if err := k.waitFor(ctx, w.String()+" to roll out", func() error { return rolledOut(ctx, client, w) }); err != nil {
			return restarted, err
		}
	}
	return restarted, nil
}

// waitFor checks until check passes or ctx ends, and then says what it waited for and why.
func (k *Kubernetes) waitFor(ctx context.Context, what string, check func() error) error {
	for {
		err := check()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %v", what, err)
		case <-time.After(k.poll()):
		}
	}
}

// rootCAHolds checks that a namespace's kube-root-ca.crt holds the CAs of the fingerprints, which
// the controller-manager publishes from the CAs it trusts.
func rootCAHolds(ctx context.Context, client kubernetes.Interface, namespace string, want []string) error {
	cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, "kube-root-ca.crt", metav1.GetOptions{})
	if err != nil {
		return err
	}
	got, err := pki.Fingerprints(cm.Data["ca.crt"])
	if err != nil {
		return err
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		return fmt.Errorf("it holds %d CA certificates, not the %d the control plane trusts", len(got), len(want))
	}
	return nil
}

// rolledOut checks that every pod of a workload runs its current template.
func rolledOut(ctx context.Context, client kubernetes.Interface, w workload) error {
	apps := client.AppsV1()
	switch w.kind {
	case "Deployment":
		d, err := apps.Deployments(w.namespace).Get(ctx, w.name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		return deploymentRolledOut(d)
	case "DaemonSet":
		d, err := apps.DaemonSets(w.namespace).Get(ctx, w.name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		st := d.Status
		if st.ObservedGeneration < d.Generation || st.UpdatedNumberScheduled != st.DesiredNumberScheduled || st.NumberAvailable != st.DesiredNumberScheduled {
			return fmt.Errorf("%d of %d pods updated and %d available", st.UpdatedNumberScheduled, st.DesiredNumberScheduled, st.NumberAvailable)
		}
	case "StatefulSet":
		s, err := apps.StatefulSets(w.namespace).Get(ctx, w.name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		st, want := s.Status, replicas(s.Spec.Replicas)
		if st.ObservedGeneration < s.Generation || st.UpdatedReplicas != want || st.ReadyReplicas != want || st.CurrentRevision != st.UpdateRevision {
			return fmt.Errorf("%d of %d pods updated and %d ready", st.UpdatedReplicas, want, st.ReadyReplicas)
		}
	}
	return nil
}

func deploymentRolledOut(d *appsv1.Deployment) error {
	st, want := d.Status, replicas(d.Spec.Replicas)
	if st.ObservedGeneration < d.Generation || st.UpdatedReplicas != want || st.AvailableReplicas != want || st.Replicas != want {
		return fmt.Errorf("%d of %d pods updated, %d available and %d in all", st.UpdatedReplicas, want, st.AvailableReplicas, st.Replicas)
	}
	return nil
}

// replicas is a workload's replica count, which is one when the specification sets none.
func replicas(n *int32) int32 {
	if n == nil {
		return 1
	}
	return *n
}

// tokenSecrets lists the Secrets of type kubernetes.io/service-account-token by namespace and
// name.
func tokenSecrets(ctx context.Context, client kubernetes.Interface) ([]string, error) {
	var names []string
	opts := metav1.ListOptions{FieldSelector: "type=" + string(corev1.SecretTypeServiceAccountToken), Limit: 250}
	for {
		list, err := client.CoreV1().Secrets(metav1.NamespaceAll).List(ctx, opts)
		if err != nil {
			return nil, err
		}
		for _, s := range list.Items {
			if s.Type == corev1.SecretTypeServiceAccountToken {
				names = append(names, s.Namespace+"/"+s.Name)
			}
		}
		if list.Continue == "" {
			break
		}
		opts.Continue = list.Continue
	}
	sort.Strings(names)
	return names, nil
}

// rewriteEncrypted updates every object of the resources the API server encrypts without
// changing it. The API server writes an object it read with another key than its first, or
// unencrypted, even when nothing changed, so it ends up encrypted with the first key.
func rewriteEncrypted(ctx context.Context, client kubernetes.Interface) (uint64, error) {
	var rewritten uint64
	for _, resource := range kpki.EncryptedResources {
		if resource != "secrets" {
			return rewritten, fmt.Errorf("chalkd rewrites secrets alone, not %s", resource)
		}
		opts := metav1.ListOptions{Limit: 250}
		for {
			list, err := client.CoreV1().Secrets(metav1.NamespaceAll).List(ctx, opts)
			if err != nil {
				return rewritten, err
			}
			for i := range list.Items {
				secret := &list.Items[i]
				err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
					_, err := client.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
					if apierrors.IsConflict(err) {
						fresh, gerr := client.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})
						if gerr != nil {
							return gerr
						}
						secret = fresh
					}
					return err
				})
				switch {
				case apierrors.IsNotFound(err):
					// Deleted since it was listed: nothing of it is left to rewrite.
				case err != nil:
					// The error names the Secret alone: its data stays out of it.
					return rewritten, fmt.Errorf("rewrite the secret %s/%s: %s", secret.Namespace, secret.Name, apierrors.ReasonForError(err))
				default:
					rewritten++
				}
			}
			if list.Continue == "" {
				break
			}
			opts.Continue = list.Continue
		}
	}
	log.Printf("rotation: rewrote %d encrypted objects", rewritten)
	return rewritten, nil
}

// countEncrypted counts the objects of the encrypted resources the node's etcd member holds,
// by the name of the key each is encrypted with, which the API server writes before the
// ciphertext: k8s:enc:<provider>:v1:<name>:.
func (k *Kubernetes) countEncrypted(ctx context.Context, c k8s.Cluster, share kpki.Share) ([]*nodev1.EncryptedObjects, error) {
	cli, err := k.dialEtcd(c, share)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	var counts []*nodev1.EncryptedObjects
	for _, resource := range kpki.EncryptedResources {
		byKey, err := countByKey(ctx, cli, "/registry/"+resource+"/")
		if err != nil {
			return nil, err
		}
		for _, key := range sortedNames(byKey) {
			counts = append(counts, &nodev1.EncryptedObjects{Resource: resource, Key: key, Objects: byKey[key]})
		}
	}
	return counts, nil
}

// countByKey counts the values below prefix by the key they are encrypted with, "" for those
// stored unencrypted. It reads them in pages, so no response holds them all.
func countByKey(ctx context.Context, cli *clientv3.Client, prefix string) (map[string]uint64, error) {
	counts := map[string]uint64{}
	end := clientv3.GetPrefixRangeEnd(prefix)
	from := prefix
	for {
		resp, err := cli.Get(ctx, from, clientv3.WithRange(end), clientv3.WithLimit(200))
		if err != nil {
			return nil, fmt.Errorf("read %s from etcd: %w", prefix, err)
		}
		for _, kv := range resp.Kvs {
			counts[EncryptionKeyOf(kv.Value)]++
		}
		if !resp.More || len(resp.Kvs) == 0 {
			return counts, nil
		}
		from = string(resp.Kvs[len(resp.Kvs)-1].Key) + "\x00"
	}
}

// EncryptionKeyOf returns the name of the key a value etcd holds is encrypted with, "" for a
// value stored unencrypted.
func EncryptionKeyOf(value []byte) string {
	if !bytes.HasPrefix(value, []byte("k8s:enc:")) {
		return ""
	}
	fields := bytes.SplitN(value, []byte(":"), 6)
	if len(fields) < 6 {
		return ""
	}
	return string(fields[4])
}

// renewKubeletServing removes the kubelet's serving certificates and restarts the kubelet, which
// requests a new one from the control plane, issued by the CA that issues now.
func (k *Kubernetes) renewKubeletServing(ctx context.Context, s *Server) error {
	files, err := filepath.Glob(filepath.Join(k.Paths.KubeletPKI, "kubelet-server-*.pem"))
	if err != nil {
		return failed(connect.CodeInternal, "%v", err)
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			return failed(connect.CodeInternal, "%v", err)
		}
	}
	if _, err := s.Run.Run(ctx, "systemctl", "restart", "kubelet.service"); err != nil {
		return failed(connect.CodeInternal, "restart the kubelet: %v", err)
	}
	log.Print("rotation: the kubelet requests a new serving certificate")
	return nil
}
