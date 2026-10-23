package apply

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/trevex/chalkos/pkg/kubernetes/revoked"
)

// policyList is the map of revoked nodes the applied policy names.
func policyList(t *testing.T, cs *fake.Clientset) string {
	t.Helper()
	p, err := cs.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(context.Background(), revoked.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range p.Spec.Variables {
		if v.Name == "revoked" {
			return v.Expression
		}
	}
	t.Fatalf("the policy names no revoked nodes: %v", p.Spec.Variables)
	return ""
}

// TestEnforceRevoked applies the policy with the nodes listed now, and its binding without
// parameters, as chalkd.
func TestEnforceRevoked(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	if err := EnforceRevoked(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if got := policyList(t, cs); got != "{}" {
		t.Errorf("without revoked nodes the policy names %s", got)
	}
	at := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	for _, name := range []string{"w1", "w2"} {
		if _, err := revoked.Revoke(ctx, cs, name, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := EnforceRevoked(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if got, want := policyList(t, cs), `{"w1": "since 2026-10-23T10:00:00Z", "w2": "since 2026-10-23T10:00:00Z"}`; got != want {
		t.Errorf("the policy names %s, want %s", got, want)
	}
	p, _ := cs.AdmissionregistrationV1().ValidatingAdmissionPolicies().Get(ctx, revoked.Name, metav1.GetOptions{})
	if p.Spec.ParamKind != nil || len(p.ManagedFields) == 0 || p.ManagedFields[0].Manager != FieldManager {
		t.Errorf("policy's parameters %v, managers %v", p.Spec.ParamKind, p.ManagedFields)
	}
	b, err := cs.AdmissionregistrationV1().ValidatingAdmissionPolicyBindings().Get(ctx, revoked.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Spec.PolicyName != revoked.Name || b.Spec.ParamRef != nil {
		t.Errorf("binding's spec = %+v", b.Spec)
	}
}

// TestEnforceRevokedFollowsChanges applies the policy again when another control plane changed
// the list while the policy was applied, so the last policy applied names the nodes listed last.
func TestEnforceRevokedFollowsChanges(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	if _, err := revoked.Revoke(ctx, cs, "w1", time.Now()); err != nil {
		t.Fatal(err)
	}
	changed := false
	cs.PrependReactor("patch", "validatingadmissionpolicies", func(k8stesting.Action) (bool, runtime.Object, error) {
		if changed {
			return false, nil, nil
		}
		changed = true
		gvr := corev1.SchemeGroupVersion.WithResource("configmaps")
		obj, err := cs.Tracker().Get(gvr, revoked.Namespace, revoked.Name)
		if err != nil {
			return true, nil, err
		}
		cm := obj.(*corev1.ConfigMap)
		cm.Data["w2"] = "2026-10-23T10:00:00Z"
		return false, nil, cs.Tracker().Update(gvr, cm, revoked.Namespace)
	})
	if err := EnforceRevoked(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if got := policyList(t, cs); !strings.Contains(got, `"w1"`) || !strings.Contains(got, `"w2"`) {
		t.Errorf("the policy names %s, want w1 and w2", got)
	}
}

// TestEnforceRevokedWithoutTheList applies nothing while the list cannot be read.
func TestEnforceRevokedWithoutTheList(t *testing.T) {
	cs := fake.NewClientset()
	cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	if err := EnforceRevoked(context.Background(), cs); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err = %v, want the read's", err)
	}
	if len(cs.Actions()) != 1 {
		t.Errorf("actions = %v, want the read alone", cs.Actions())
	}
}
