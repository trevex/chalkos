package chalkd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"connectrpc.com/connect"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	kubernetesv1 "github.com/trevex/chalkos/pkg/api/kubernetes/v1"
	k8s "github.com/trevex/chalkos/pkg/kubernetes"
	"github.com/trevex/chalkos/pkg/kubernetes/etcd"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// RestartedAnnotation is the annotation of a workload's pod template that restarting it for a
// rotation sets, as kubectl rollout restart sets its own.
const RestartedAnnotation = "chalkos.dev/restarted-at"

func (p *kubernetesLayer) RotationStep(ctx context.Context, req *connect.Request[kubernetesv1.RotationStepRequest]) (*connect.Response[kubernetesv1.RotationStepResponse], error) {
	k := p.k
	// An unknown step is refused before it gets a credential for the API server.
	var step func(context.Context, *kubernetesv1.RotationStepResponse) error
	switch req.Msg.Step {
	case kubernetesv1.RotationStep_ROTATION_STEP_RENEW_KUBELET_SERVING:
		step = p.renewKubeletServing
	case kubernetesv1.RotationStep_ROTATION_STEP_COUNT_ENCRYPTED:
		step = k.countEncryptedStep
	case kubernetesv1.RotationStep_ROTATION_STEP_RESTART_ADDONS:
		step = k.restartAddonsStep
	case kubernetesv1.RotationStep_ROTATION_STEP_LIST_TOKEN_SECRETS:
		step = k.tokenSecretsStep
	case kubernetesv1.RotationStep_ROTATION_STEP_REWRITE_ENCRYPTED:
		step = k.rewriteEncryptedStep
	default:
		return nil, failed(connect.CodeInvalidArgument, "unknown rotation step %v", req.Msg.Step)
	}
	resp := &kubernetesv1.RotationStepResponse{}
	if err := step(ctx, resp); err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// stepFailed refuses a rotation step that failed, unless err already says how.
func stepFailed(err error) error {
	var ce *connect.Error
	if err == nil || errors.As(err, &ce) {
		return err
	}
	return failed(connect.CodeFailedPrecondition, "%v", err)
}

// countEncryptedStep counts the encrypted objects the node's etcd member holds by key.
func (k *Kubernetes) countEncryptedStep(ctx context.Context, resp *kubernetesv1.RotationStepResponse) error {
	cp, err := k.etcdMember()
	if err != nil {
		return err
	}
	resp.Encrypted, err = k.countEncrypted(ctx, cp.cluster, cp.share)
	return stepFailed(err)
}

// restartAddonsStep restarts the workloads of the cluster's manifests; see restartAddons.
func (k *Kubernetes) restartAddonsStep(ctx context.Context, resp *kubernetesv1.RotationStepResponse) error {
	client, cp, err := k.apiClient(k.etcdMember)
	if err != nil {
		return err
	}
	resp.Restarted, resp.NotWaited, err = k.restartAddons(ctx, cp.cluster, client, cp.share.CABundle())
	return stepFailed(err)
}

// tokenSecretsStep lists the cluster's service account token Secrets.
func (k *Kubernetes) tokenSecretsStep(ctx context.Context, resp *kubernetesv1.RotationStepResponse) error {
	client, _, err := k.apiClient(k.etcdMember)
	if err != nil {
		return err
	}
	resp.TokenSecrets, err = tokenSecrets(ctx, client)
	return stepFailed(err)
}

// rewriteEncryptedStep rewrites the encrypted objects, then counts them by key.
func (k *Kubernetes) rewriteEncryptedStep(ctx context.Context, resp *kubernetesv1.RotationStepResponse) error {
	client, cp, err := k.apiClient(k.etcdMember)
	if err != nil {
		return err
	}
	if resp.Rewritten, err = rewriteEncrypted(ctx, client); err == nil {
		resp.Encrypted, err = k.countEncrypted(ctx, cp.cluster, cp.share)
	}
	return stepFailed(err)
}

// workload is a Deployment, DaemonSet or StatefulSet of the cluster's manifests.
type workload struct{ kind, namespace, name string }

func (w workload) String() string { return w.namespace + "/" + w.kind + "/" + w.name }

// restartAddons restarts the workloads of the cluster's manifests, chalkos's add-ons among them,
// so their pods trust the Kubernetes CAs of bundle: a pod reads its service account's CA
// certificates when it starts. They restart only once kube-root-ca.crt holds all of them in
// their namespaces, and the step ends once they rolled out. A workload whose rollout does not
// complete by itself, such as one that updates its pods only when they are deleted, is restarted
// and returned in notWaited instead of being waited for.
func (k *Kubernetes) restartAddons(ctx context.Context, c k8s.Cluster, client kubernetes.Interface, bundle string) (restarted, notWaited []string, err error) {
	objects, err := k.hooks.Objects(c)
	if err != nil {
		return nil, nil, err
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
		return nil, nil, err
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
			return nil, nil, err
		}
	}
	patch := fmt.Appendf(nil, `{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`, RestartedAnnotation, time.Now().UTC().Format(time.RFC3339))
	var rollouts []workload
	for _, w := range workloads {
		apps := client.AppsV1()
		var rollsOut bool
		var err error
		switch w.kind {
		case "Deployment":
			var d *appsv1.Deployment
			if d, err = apps.Deployments(w.namespace).Patch(ctx, w.name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err == nil {
				rollsOut = !d.Spec.Paused
			}
		case "DaemonSet":
			var d *appsv1.DaemonSet
			if d, err = apps.DaemonSets(w.namespace).Patch(ctx, w.name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err == nil {
				rollsOut = d.Spec.UpdateStrategy.Type != appsv1.OnDeleteDaemonSetStrategyType
			}
		case "StatefulSet":
			var s *appsv1.StatefulSet
			if s, err = apps.StatefulSets(w.namespace).Patch(ctx, w.name, types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err == nil {
				u := s.Spec.UpdateStrategy
				rollsOut = u.Type != appsv1.OnDeleteStatefulSetStrategyType && (u.RollingUpdate == nil || u.RollingUpdate.Partition == nil || *u.RollingUpdate.Partition == 0)
			}
		}
		if err != nil {
			return restarted, notWaited, fmt.Errorf("restart %s: %w", w, err)
		}
		log.Printf("rotation: restarted %s", w)
		restarted = append(restarted, w.String())
		if rollsOut {
			rollouts = append(rollouts, w)
		} else {
			notWaited = append(notWaited, w.String())
		}
	}
	for _, w := range rollouts {
		if err := k.waitFor(ctx, w.String()+" to roll out", func() error { return rolledOut(ctx, client, w) }); err != nil {
			return restarted, notWaited, err
		}
	}
	return restarted, notWaited, nil
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
		case <-time.After(k.timing.Poll):
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
		return fmt.Errorf("it holds %s, not the %d the control plane trusts", count(len(got), "CA certificate"), len(want))
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

// rewriteRestarts bounds how often the rewrite starts over when the list it pages through
// expired, as one does once etcd compacted the revision the list began at.
const rewriteRestarts = 3

// rewriteEncrypted updates every object of the resources the API server encrypts without
// changing it. The API server writes an object it read with another key than its first, or
// unencrypted, even when nothing changed, so it ends up encrypted with the first key.
func rewriteEncrypted(ctx context.Context, client kubernetes.Interface) (uint64, error) {
	var rewritten uint64
	for _, resource := range kpki.EncryptedResources {
		if resource != "secrets" {
			return rewritten, fmt.Errorf("chalkd rewrites secrets alone, not %s", resource)
		}
		// done holds the Secrets rewritten or gone, which a list that starts over skips.
		done := map[string]bool{}
		restarts := 0
		opts := metav1.ListOptions{Limit: 250}
		for {
			list, err := client.CoreV1().Secrets(metav1.NamespaceAll).List(ctx, opts)
			if opts.Continue != "" && (apierrors.IsResourceExpired(err) || apierrors.IsGone(err)) && restarts < rewriteRestarts {
				restarts++
				log.Printf("rotation: the list of secrets expired; listing them again, skipping the %d done", len(done))
				opts.Continue = ""
				continue
			}
			if err != nil {
				return rewritten, err
			}
			for i := range list.Items {
				secret := &list.Items[i]
				id := secret.Namespace + "/" + secret.Name
				if done[id] {
					continue
				}
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
					done[id] = true
				case err != nil:
					return rewritten, fmt.Errorf("rewrite the secret %s: %s", id, whyNot(err))
				default:
					done[id] = true
					rewritten++
				}
			}
			if list.Continue == "" {
				break
			}
			opts.Continue = list.Continue
		}
	}
	log.Printf("rotation: rewrote %s", count(rewritten, "encrypted object"))
	return rewritten, nil
}

// whyNot says why the API server refused a request about a Secret: the reason of its status,
// else its HTTP status, without its message, which may echo the Secret's data; or the error of a
// request that never got an answer, which holds no data.
func whyNot(err error) string {
	if reason := apierrors.ReasonForError(err); reason != metav1.StatusReasonUnknown {
		return string(reason)
	}
	if status, ok := err.(apierrors.APIStatus); ok || errors.As(err, &status) {
		return fmt.Sprintf("HTTP %d", status.Status().Code)
	}
	return err.Error()
}

// countEncrypted counts the objects of the encrypted resources the node's etcd member holds,
// by the name of the key each is encrypted with, which the API server writes before the
// ciphertext: k8s:enc:<provider>:v1:<name>:.
func (k *Kubernetes) countEncrypted(ctx context.Context, c k8s.Cluster, share kpki.Share) ([]*kubernetesv1.EncryptedObjects, error) {
	cli, err := k.dialEtcd(c, share)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	var counts []*kubernetesv1.EncryptedObjects
	for _, resource := range kpki.EncryptedResources {
		byKey, err := etcd.CountByKey(ctx, cli, "/registry/"+resource+"/")
		if err != nil {
			return nil, err
		}
		for _, key := range slices.Sorted(maps.Keys(byKey)) {
			counts = append(counts, &kubernetesv1.EncryptedObjects{Resource: resource, Key: key, Objects: byKey[key]})
		}
	}
	return counts, nil
}

// renewKubeletServing removes the kubelet's serving certificates and restarts the kubelet, which
// requests a new one from the control plane, issued by the CA that issues now.
func (p *kubernetesLayer) renewKubeletServing(ctx context.Context, _ *kubernetesv1.RotationStepResponse) error {
	p.node.Changes().Lock()
	defer p.node.Changes().Unlock()
	k := p.k
	files, err := filepath.Glob(filepath.Join(k.Paths.KubeletPKI, "kubelet-server-*.pem"))
	if err != nil {
		return failed(connect.CodeInternal, "%v", err)
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil {
			return failed(connect.CodeInternal, "%v", err)
		}
	}
	if _, err := p.node.Runner().Run(ctx, "systemctl", "restart", "kubelet.service"); err != nil {
		return failed(connect.CodeInternal, "restart the kubelet: %v", err)
	}
	log.Print("rotation: the kubelet requests a new serving certificate")
	return nil
}
