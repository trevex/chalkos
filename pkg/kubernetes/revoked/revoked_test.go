package revoked

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

var (
	first  = time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)
	second = first.Add(time.Hour)
)

func load(t *testing.T, cs *fake.Clientset) List {
	t.Helper()
	l, err := Load(context.Background(), cs)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLoadWithoutConfigMap(t *testing.T) {
	if l := load(t, fake.NewClientset()); len(l) != 0 {
		t.Errorf("list %v, want it empty", l)
	}
}

// TestBadTime keeps a node whose time is no RFC 3339 revoked, at an unknown time, without
// failing the rest of the list. Revoking it again records a time; unrevoking it removes it.
func TestBadTime(t *testing.T) {
	ctx := context.Background()
	bad := func() *fake.Clientset {
		return fake.NewClientset(&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: Name},
			Data:       map[string]string{"w1": "yesterday", "w2": first.Format(TimeFormat)},
		})
	}
	cs := bad()
	l := load(t, cs)
	if at, ok := l["w1"]; !ok || !at.IsZero() || !l["w2"].Equal(first) {
		t.Errorf("list %v, want w1 at an unknown time and w2 at %v", l, first)
	}
	if at, err := Revoke(ctx, cs, "w1", second); err != nil || !at.Equal(second) {
		t.Errorf("revoking w1 again: %v, %v; want %v", at, err, second)
	}
	if l := load(t, cs); !l["w1"].Equal(second) {
		t.Errorf("list %v, want w1 at %v", l, second)
	}
	cs = bad()
	if at, listed, err := Unrevoke(ctx, cs, "w1", nil); err != nil || !listed || !at.IsZero() {
		t.Errorf("unrevoking w1: %v, listed %v, %v", at, listed, err)
	}
	if l := load(t, cs); len(l) != 1 {
		t.Errorf("list %v, want w2 alone", l)
	}
}

// TestUnrevokeAllows removes a node only when allow accepts the time it is listed with when it
// is removed, though another control plane changed it since the first read.
func TestUnrevokeAllows(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	if _, err := Revoke(ctx, cs, "w1", first); err != nil {
		t.Fatal(err)
	}
	refusal := errors.New("refused")
	if _, _, err := Unrevoke(ctx, cs, "w1", func(time.Time) error { return refusal }); !errors.Is(err, refusal) {
		t.Fatalf("unrevoke refused: %v", err)
	}
	if _, ok := load(t, cs)["w1"]; !ok {
		t.Fatal("a refused unrevoke removed w1")
	}
	changed := false
	cs.PrependReactor("update", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		if changed {
			return false, nil, nil
		}
		changed = true
		cm, err := cs.Tracker().Get(corev1.SchemeGroupVersion.WithResource("configmaps"), Namespace, Name)
		if err != nil {
			return true, nil, err
		}
		other := cm.(*corev1.ConfigMap).DeepCopy()
		other.Data["w1"] = second.Format(TimeFormat)
		if err := cs.Tracker().Update(corev1.SchemeGroupVersion.WithResource("configmaps"), other, Namespace); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, Name, nil)
	})
	var allowed []time.Time
	at, listed, err := Unrevoke(ctx, cs, "w1", func(at time.Time) error {
		allowed = append(allowed, at)
		return nil
	})
	if err != nil || !listed || !at.Equal(second) {
		t.Fatalf("unrevoke: %v, listed %v, %v; want %v", at, listed, err, second)
	}
	if len(allowed) != 2 || !allowed[1].Equal(second) {
		t.Errorf("allow saw %v, want %v last", allowed, second)
	}
}

// TestRevoke records the first revocation of a name; revoking it again keeps that time.
func TestRevoke(t *testing.T) {
	cs := fake.NewClientset()
	ctx := context.Background()
	at, err := Revoke(ctx, cs, "w1", first)
	if err != nil || !at.Equal(first) {
		t.Fatalf("revoke: %v, %v; want %v", at, err, first)
	}
	if at, err = Revoke(ctx, cs, "w1", second); err != nil || !at.Equal(first) {
		t.Fatalf("revoke again: %v, %v; want the first time %v", at, err, first)
	}
	if _, err := Revoke(ctx, cs, "w2", second); err != nil {
		t.Fatal(err)
	}
	l := load(t, cs)
	if len(l) != 2 || !l["w1"].Equal(first) || !l["w2"].Equal(second) {
		t.Errorf("list %v, want w1 at %v and w2 at %v", l, first, second)
	}
	cm, err := cs.CoreV1().ConfigMaps(Namespace).Get(ctx, Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := cm.Data["w1"]; got != "2026-10-23T08:00:00Z" {
		t.Errorf("w1 stored as %q, want RFC 3339", got)
	}
}

func TestUnrevoke(t *testing.T) {
	cs := fake.NewClientset()
	ctx := context.Background()
	if _, listed, err := Unrevoke(ctx, cs, "w1", nil); err != nil || listed {
		t.Fatalf("unrevoke without a list: listed %v, %v", listed, err)
	}
	for _, name := range []string{"w1", "w2"} {
		if _, err := Revoke(ctx, cs, name, first); err != nil {
			t.Fatal(err)
		}
	}
	if _, listed, err := Unrevoke(ctx, cs, "w3", nil); err != nil || listed {
		t.Fatalf("unrevoke an unknown name: listed %v, %v", listed, err)
	}
	at, listed, err := Unrevoke(ctx, cs, "w1", nil)
	if err != nil || !listed || !at.Equal(first) {
		t.Fatalf("unrevoke w1: %v, listed %v, %v", at, listed, err)
	}
	if l := load(t, cs); len(l) != 1 || !l["w2"].Equal(first) {
		t.Errorf("list %v, want w2 alone", l)
	}
}

// TestRevokeRetriesConflicts revokes again when another control plane updated the list since it
// was read, and keeps the other's entry.
func TestRevokeRetriesConflicts(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	if _, err := Revoke(ctx, cs, "w1", first); err != nil {
		t.Fatal(err)
	}
	conflicts := 0
	cs.PrependReactor("update", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts > 0 {
			return false, nil, nil
		}
		conflicts++
		// Another revoke lands between this one's read and its update.
		cm, err := cs.Tracker().Get(corev1.SchemeGroupVersion.WithResource("configmaps"), Namespace, Name)
		if err != nil {
			return true, nil, err
		}
		other := cm.(*corev1.ConfigMap).DeepCopy()
		other.Data["w3"] = first.Format(time.RFC3339)
		if err := cs.Tracker().Update(corev1.SchemeGroupVersion.WithResource("configmaps"), other, Namespace); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, Name, nil)
	})
	if _, err := Revoke(ctx, cs, "w2", second); err != nil {
		t.Fatal(err)
	}
	if conflicts != 1 {
		t.Errorf("%d conflicts, want 1", conflicts)
	}
	if l := load(t, cs); len(l) != 3 {
		t.Errorf("list %v, want w1, w2 and w3", l)
	}
}

// TestRevokeRetriesACreateRace revokes again when another control plane created the list first.
func TestRevokeRetriesACreateRace(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	raced := false
	cs.PrependReactor("create", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		if raced {
			return false, nil, nil
		}
		raced = true
		other := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: Name},
			Data:       map[string]string{"w3": first.Format(time.RFC3339)},
		}
		if err := cs.Tracker().Add(other); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewAlreadyExists(schema.GroupResource{Resource: "configmaps"}, Name)
	})
	if _, err := Revoke(ctx, cs, "w1", first); err != nil {
		t.Fatal(err)
	}
	if l := load(t, cs); len(l) != 2 {
		t.Errorf("list %v, want w1 and w3", l)
	}
}
