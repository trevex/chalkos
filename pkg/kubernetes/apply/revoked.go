package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	admissionv1ac "k8s.io/client-go/applyconfigurations/admissionregistration/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/trevex/chalkos/pkg/kubernetes/addons"
	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
)

// enforceAttempts bounds how often EnforceRevoked applies the policy while the list keeps
// changing.
const enforceAttempts = 5

// EnforceRevoked applies the admission policy that shuts out the revoked nodes, naming those
// listed now, and its binding. Another control plane may change the list and apply its own
// policy meanwhile, and the last policy applied wins; so the list is read again after each
// apply, and the policy applied again until it names the nodes listed last.
func EnforceRevoked(ctx context.Context, cs kubernetes.Interface) error {
	var applied revoked.List
	for range enforceAttempts {
		l, err := revoked.Load(ctx, cs)
		if err != nil {
			return err
		}
		if applied != nil && maps.EqualFunc(l, applied, time.Time.Equal) {
			return nil
		}
		if err := applyRevoked(ctx, cs, l); err != nil {
			return err
		}
		applied = l
	}
	return fmt.Errorf("the revoked nodes changed %d times while their admission policy was applied; apply it again", enforceAttempts)
}

// applyRevoked applies the policy naming the list, then its binding, with server-side apply.
func applyRevoked(ctx context.Context, cs kubernetes.Interface, l revoked.List) error {
	objects := addons.RevokedNodes(l)
	opts := metav1.ApplyOptions{FieldManager: FieldManager, Force: true}
	var policy admissionv1ac.ValidatingAdmissionPolicyApplyConfiguration
	if err := convert(objects[0], &policy); err != nil {
		return err
	}
	if _, err := cs.AdmissionregistrationV1().ValidatingAdmissionPolicies().Apply(ctx, &policy, opts); err != nil {
		return fmt.Errorf("apply the revoked nodes' admission policy: %w", err)
	}
	var binding admissionv1ac.ValidatingAdmissionPolicyBindingApplyConfiguration
	if err := convert(objects[1], &binding); err != nil {
		return err
	}
	if _, err := cs.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Apply(ctx, &binding, opts); err != nil {
		return fmt.Errorf("apply the revoked nodes' admission policy binding: %w", err)
	}
	return nil
}

// convert turns a rendered object into its apply configuration.
func convert(object map[string]any, out any) error {
	data, err := json.Marshal(object)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}
