package revoked

import (
	"context"
	"errors"
	"slices"
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

const year = 365 * 24 * time.Hour

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
	if at, err := Revoke(ctx, cs, "w1", second, year); err != nil || !at.Equal(second) {
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
	if _, err := Revoke(ctx, cs, "w1", first, year); err != nil {
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

// TestRevoke records the first revocation of a name, with the time its entry expires; revoking
// it again keeps that time.
func TestRevoke(t *testing.T) {
	cs := fake.NewClientset()
	ctx := context.Background()
	at, err := Revoke(ctx, cs, "w1", first, year)
	if err != nil || !at.Equal(first) {
		t.Fatalf("revoke: %v, %v; want %v", at, err, first)
	}
	if at, err = Revoke(ctx, cs, "w1", second, year); err != nil || !at.Equal(first) {
		t.Fatalf("revoke again: %v, %v; want the first time %v", at, err, first)
	}
	if _, err := Revoke(ctx, cs, "w2", second, year); err != nil {
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
	if got := cm.Data["w1"]; got != "2026-10-23T08:00:00Z/2027-10-25T08:00:00Z" {
		t.Errorf("w1 stored as %q, want its time and expiry in RFC 3339", got)
	}
}

// TestRefresh lists a revoked node with the later of its time and now, and leaves a node not
// revoked unlisted.
func TestRefresh(t *testing.T) {
	cs := fake.NewClientset()
	ctx := context.Background()
	if _, listed, err := Refresh(ctx, cs, "w1", second, year); err != nil || listed {
		t.Fatalf("refresh without a list: listed %v, %v", listed, err)
	}
	if _, ok := load(t, cs)["w1"]; ok {
		t.Fatal("refreshing a node not revoked lists it")
	}
	for _, name := range []string{"w1", "w2"} {
		if _, err := Revoke(ctx, cs, name, first, year); err != nil {
			t.Fatal(err)
		}
	}
	if at, listed, err := Refresh(ctx, cs, "w1", second.Add(time.Millisecond), year); err != nil || !listed || !at.Equal(second) {
		t.Fatalf("refresh w1: %v, listed %v, %v; want %v", at, listed, err, second)
	}
	if at, listed, err := Refresh(ctx, cs, "w1", first, year); err != nil || !listed || !at.Equal(second) {
		t.Fatalf("refresh w1 with an earlier time: %v, listed %v, %v; want %v kept", at, listed, err, second)
	}
	if _, listed, err := Refresh(ctx, cs, "w3", second, year); err != nil || listed {
		t.Fatalf("refresh an unknown name: listed %v, %v", listed, err)
	}
	if l := load(t, cs); len(l) != 2 || !l["w1"].Equal(second) || !l["w2"].Equal(first) {
		t.Errorf("list %v, want w1 at %v and w2 at %v", l, second, first)
	}
}

func TestUnrevoke(t *testing.T) {
	cs := fake.NewClientset()
	ctx := context.Background()
	if _, listed, err := Unrevoke(ctx, cs, "w1", nil); err != nil || listed {
		t.Fatalf("unrevoke without a list: listed %v, %v", listed, err)
	}
	for _, name := range []string{"w1", "w2"} {
		if _, err := Revoke(ctx, cs, name, first, year); err != nil {
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
	if _, err := Revoke(ctx, cs, "w1", first, year); err != nil {
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
	if _, err := Revoke(ctx, cs, "w2", second, year); err != nil {
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
	if _, err := Revoke(ctx, cs, "w1", first, year); err != nil {
		t.Fatal(err)
	}
	if l := load(t, cs); len(l) != 2 {
		t.Errorf("list %v, want w1 and w3", l)
	}
}

// TestExpired expires a node's entry once its stored expiry has passed, or, for an entry stored
// without one, once its certificates, valid for validity, and the margin have; one revoked at an
// unknown time never expires.
func TestExpired(t *testing.T) {
	for name, tc := range map[string]struct {
		e        Entry
		now      time.Time
		validity time.Duration
		want     bool
	}{
		"within the validity":        {Entry{At: first}, first.Add(year), year, false},
		"within the margin":          {Entry{At: first}, first.Add(year + expiryMargin), year, false},
		"past validity and margin":   {Entry{At: first}, first.Add(year + expiryMargin + time.Second), year, true},
		"a longer validity":          {Entry{At: first}, first.Add(year + expiryMargin + time.Second), 2 * year, false},
		"revoked at an unknown time": {Entry{}, first.Add(100 * year), year, false},
		"before the stored expiry":   {Entry{At: first, Expires: first.Add(2 * year)}, first.Add(2 * year), year, false},
		"past the stored expiry":     {Entry{At: first, Expires: first.Add(year)}, first.Add(year + time.Second), 2 * year, true},
	} {
		if got := tc.e.Expired(tc.now, tc.validity); got != tc.want {
			t.Errorf("%s: Expired = %v, want %v", name, got, tc.want)
		}
	}
}

// entries reads the stored entries.
func entries(t *testing.T, cs *fake.Clientset) map[string]Entry {
	t.Helper()
	cm, err := cs.CoreV1().ConfigMaps(Namespace).Get(context.Background(), Name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return parse(cm)
}

// TestExpiryNeverShrinks keeps an entry's expiry when the node is revoked or refreshed again
// under a shorter validity, as after the cluster lowered cluster-signing-duration, and moves it
// later under a longer one or a later refresh.
func TestExpiryNeverShrinks(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	if _, err := Revoke(ctx, cs, "w1", first, 2*year); err != nil {
		t.Fatal(err)
	}
	long := first.Add(2*year + expiryMargin)
	if _, err := Revoke(ctx, cs, "w1", second, year); err != nil {
		t.Fatal(err)
	}
	if got := entries(t, cs)["w1"]; !got.At.Equal(first) || !got.Expires.Equal(long) {
		t.Errorf("revoked again under a shorter validity: %+v, want expiry %v", got, long)
	}
	if _, _, err := Refresh(ctx, cs, "w1", second, year); err != nil {
		t.Fatal(err)
	}
	if got := entries(t, cs)["w1"]; !got.At.Equal(second) || !got.Expires.Equal(long) {
		t.Errorf("refreshed under a shorter validity: %+v, want at %v, expiry %v", got, second, long)
	}
	if _, _, err := Refresh(ctx, cs, "w1", second, 3*year); err != nil {
		t.Fatal(err)
	}
	if got, want := entries(t, cs)["w1"].Expires, second.Add(3*year+expiryMargin); !got.Equal(want) {
		t.Errorf("refreshed under a longer validity: expiry %v, want %v", got, want)
	}
}

// TestEntryWithoutExpiry reads an entry stored with its time alone, as before entries kept their
// expiry, or with a broken expiry, without one; revoking the node again stores one.
func TestEntryWithoutExpiry(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: Name},
		Data:       map[string]string{"w1": first.Format(TimeFormat), "w2": first.Format(TimeFormat) + "/soon"},
	})
	for _, name := range []string{"w1", "w2"} {
		if got := entries(t, cs)[name]; !got.At.Equal(first) || !got.Expires.IsZero() {
			t.Errorf("%s: %+v, want at %v without an expiry", name, got, first)
		}
		if !load(t, cs)[name].Equal(first) {
			t.Errorf("%s: list %v, want it at %v", name, load(t, cs), first)
		}
	}
	if _, err := Revoke(ctx, cs, "w1", second, year); err != nil {
		t.Fatal(err)
	}
	if got, want := entries(t, cs)["w1"], first.Add(year+expiryMargin); !got.At.Equal(first) || !got.Expires.Equal(want) {
		t.Errorf("revoked again: %+v, want at %v, expiry %v", got, first, want)
	}
}

// TestPrune removes the entries drop names, in name order, and keeps the others; a list without
// such entries, or without a ConfigMap, is not written.
func TestPrune(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	if removed, err := Prune(ctx, cs, func(string, Entry) bool { return true }); err != nil || len(removed) != 0 {
		t.Fatalf("prune without a ConfigMap: %v, %v", removed, err)
	}
	if _, err := cs.CoreV1().ConfigMaps(Namespace).Get(ctx, Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("prune without a ConfigMap created one: %v", err)
	}
	for _, name := range []string{"w3", "w1", "w2"} {
		if _, err := Revoke(ctx, cs, name, first, year); err != nil {
			t.Fatal(err)
		}
	}
	writes := 0
	cs.PrependReactor("update", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		writes++
		return false, nil, nil
	})
	removed, err := Prune(ctx, cs, func(name string, e Entry) bool { return name != "w2" && e.At.Equal(first) })
	if err != nil || !slices.Equal(removed, []string{"w1", "w3"}) {
		t.Fatalf("Prune = %v, %v; want w1 and w3", removed, err)
	}
	if l := load(t, cs); len(l) != 1 || !l["w2"].Equal(first) {
		t.Errorf("left %v; want w2 alone", l)
	}
	if removed, err := Prune(ctx, cs, func(name string, _ Entry) bool { return name == "w9" }); err != nil || len(removed) != 0 || writes != 1 {
		t.Errorf("a prune that drops nothing: %v, %v, %d writes; want none and no second write", removed, err, writes)
	}
}

// TestPruneRetriesConflicts prunes again from what another control plane wrote since the list
// was read: an entry it removed is not reported, and one it added is judged too.
func TestPruneRetriesConflicts(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewClientset()
	for _, name := range []string{"w1", "w2", "w3"} {
		at := second
		if name != "w2" {
			at = first
		}
		if _, err := Revoke(ctx, cs, name, at, year); err != nil {
			t.Fatal(err)
		}
	}
	conflicts := 0
	cs.PrependReactor("update", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		if conflicts > 0 {
			return false, nil, nil
		}
		conflicts++
		// Another control plane revokes w4 and unrevokes w1 between this prune's read and its update.
		cm, err := cs.Tracker().Get(corev1.SchemeGroupVersion.WithResource("configmaps"), Namespace, Name)
		if err != nil {
			return true, nil, err
		}
		other := cm.(*corev1.ConfigMap).DeepCopy()
		other.Data["w4"] = second.Format(TimeFormat)
		delete(other.Data, "w1")
		if err := cs.Tracker().Update(corev1.SchemeGroupVersion.WithResource("configmaps"), other, Namespace); err != nil {
			return true, nil, err
		}
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, Name, nil)
	})
	removed, err := Prune(ctx, cs, func(_ string, e Entry) bool { return e.At.Equal(first) })
	if err != nil || !slices.Equal(removed, []string{"w3"}) {
		t.Fatalf("Prune = %v, %v; want w3 alone", removed, err)
	}
	if conflicts != 1 {
		t.Errorf("%d conflicts, want 1", conflicts)
	}
	if l := load(t, cs); len(l) != 2 || !l["w2"].Equal(second) || !l["w4"].Equal(second) {
		t.Errorf("left %v; want w2 and w4", l)
	}
}
