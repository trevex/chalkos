// Package revoked keeps the cluster's revoked nodes in a ConfigMap: the names of nodes that are
// cut off, with the time each was revoked. Control planes read it before they renew a node
// certificate, and the admission policy that shuts the nodes out names them.
package revoked

import (
	"context"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

// The ConfigMap's data maps each revoked node's name to its revocation time in TimeFormat and,
// after a slash, the time its entry expires, as an ISO 8601 interval:
// 2026-10-23T08:00:00Z/2027-10-24T08:00:00Z. An entry stored with its time alone has no stored
// expiry.
const (
	Namespace  = metav1.NamespaceSystem
	Name       = "chalkos-revoked-nodes"
	TimeFormat = time.RFC3339
)

// List maps the names of revoked nodes to the times they were revoked. A node whose time cannot
// be read is revoked at an unknown time, the zero time.
type List map[string]time.Time

// Entry is a revoked node's entry: the time the node was revoked, the zero time if unknown, and
// the time the entry expires, the zero time if it has no stored expiry.
type Entry struct {
	At, Expires time.Time
}

// Load reads the list; a cluster without the ConfigMap has revoked no node.
func Load(ctx context.Context, cs kubernetes.Interface) (List, error) {
	cm, err := cs.CoreV1().ConfigMaps(Namespace).Get(ctx, Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return List{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the revoked nodes: %w", err)
	}
	l := List{}
	for name, e := range parse(cm) {
		l[name] = e.At
	}
	return l, nil
}

// parse reads the entries. An entry whose time is broken, as by hand, keeps its node revoked and
// leaves the others readable; one whose expiry is broken has no stored expiry.
func parse(cm *corev1.ConfigMap) map[string]Entry {
	entries := map[string]Entry{}
	for name, value := range cm.Data {
		at, expires, _ := strings.Cut(value, "/")
		var e Entry
		var err error
		if e.At, err = time.Parse(TimeFormat, at); err != nil {
			log.Printf("the revoked nodes: %s is revoked at an unknown time: %v", name, err)
		}
		if expires != "" {
			if e.Expires, err = time.Parse(TimeFormat, expires); err != nil {
				log.Printf("the revoked nodes: the expiry of %s is unknown: %v", name, err)
			}
		}
		entries[name] = e
	}
	return entries
}

// set lists the node as revoked at, its entry expiring at Expiry(at, validity) or at the expiry
// the entry has, whichever is later. It reports whether the ConfigMap changed.
func set(cm *corev1.ConfigMap, entries map[string]Entry, name string, at time.Time, validity time.Duration) bool {
	e := Entry{At: at, Expires: entries[name].Expires}
	if expires := Expiry(at, validity); expires.After(e.Expires) {
		e.Expires = expires
	}
	value := e.At.Format(TimeFormat)
	if !e.Expires.IsZero() {
		value += "/" + e.Expires.UTC().Format(TimeFormat)
	}
	if old, ok := cm.Data[name]; ok && old == value {
		return false
	}
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[name] = value
	return true
}

// Revoke lists the node as revoked at now and returns the time it is listed with: that of an
// earlier revocation, if there was one. An unknown time is replaced by now, which only makes
// more CAs predate the revocation. The entry expires once certificates valid for validity from
// that time have, unless it expires later already: a lower validity shortens no entry.
func Revoke(ctx context.Context, cs kubernetes.Interface, name string, now time.Time, validity time.Duration) (time.Time, error) {
	var at time.Time
	err := update(ctx, cs, func(cm *corev1.ConfigMap, entries map[string]Entry) (bool, error) {
		if at = entries[name].At; at.IsZero() {
			at = now.UTC().Truncate(time.Second)
		}
		return set(cm, entries, name, at, validity), nil
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("revoke %s: %w", name, err)
	}
	return at, nil
}

// Refresh lists a node revoked already as revoked at now, or keeps its time if that is later, as
// another control plane's clock may set it; it returns the time the node is listed with and
// whether it was listed at all. A node not listed stays unlisted. A node handed certificates while
// revoked stays listed, refreshed after them, until they expire too: its entry expires once
// certificates valid for validity from the new time have, unless it expires later already.
func Refresh(ctx context.Context, cs kubernetes.Interface, name string, now time.Time, validity time.Duration) (time.Time, bool, error) {
	var at time.Time
	var listed bool
	err := update(ctx, cs, func(cm *corev1.ConfigMap, entries map[string]Entry) (bool, error) {
		var earlier Entry
		if earlier, listed = entries[name]; !listed {
			return false, nil
		}
		if at = now.UTC().Truncate(time.Second); !earlier.At.Before(at) {
			at = earlier.At
		}
		return set(cm, entries, name, at, validity), nil
	})
	if err != nil {
		return time.Time{}, false, fmt.Errorf("refresh the revocation of %s: %w", name, err)
	}
	return at, listed, nil
}

// Unrevoke removes the node from the list if allow, unless nil, accepts the time it is listed
// with when it is removed; allow's error is returned as it is. Unrevoke returns that time and
// whether the node was listed at all.
func Unrevoke(ctx context.Context, cs kubernetes.Interface, name string, allow func(at time.Time) error) (time.Time, bool, error) {
	var at time.Time
	var listed bool
	var refused error
	err := update(ctx, cs, func(cm *corev1.ConfigMap, entries map[string]Entry) (bool, error) {
		var e Entry
		if e, listed = entries[name]; !listed {
			return false, nil
		}
		at = e.At
		if allow != nil {
			if refused = allow(at); refused != nil {
				return false, refused
			}
		}
		delete(cm.Data, name)
		return true, nil
	})
	if refused != nil {
		return at, listed, refused
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("unrevoke %s: %w", name, err)
	}
	return at, listed, nil
}

// expiryMargin is how long after its certificates' validity a revoked node's entry is kept: a
// renewal in flight when the node was revoked may have issued a certificate a moment later.
const expiryMargin = 24 * time.Hour

// Expiry is the time every certificate of a node revoked at has expired, when they are valid for
// validity: a revoked node renews none, so its last was issued at the revocation at the latest,
// give or take expiryMargin. For a node revoked at an unknown time, the zero time, it is the zero
// time.
func Expiry(at time.Time, validity time.Duration) time.Time {
	if at.IsZero() {
		return time.Time{}
	}
	return at.Add(validity + expiryMargin)
}

// Expired reports whether the entry has expired by now: past its stored expiry or, without one,
// past its Expiry for validity. A node revoked at an unknown time never expires.
func (e Entry) Expired(now time.Time, validity time.Duration) bool {
	if e.At.IsZero() {
		return false
	}
	expires := e.Expires
	if expires.IsZero() {
		expires = Expiry(e.At, validity)
	}
	return now.After(expires)
}

// Prune removes the nodes for which drop reports true from the list and returns their names in
// name order. It writes the ConfigMap only when it removes a node. Another control plane may
// write the list in between: the prune then starts over from what that one wrote; drop may be
// asked again for the same node.
func Prune(ctx context.Context, cs kubernetes.Interface, drop func(name string, e Entry) bool) ([]string, error) {
	var removed []string
	err := update(ctx, cs, func(cm *corev1.ConfigMap, entries map[string]Entry) (bool, error) {
		removed = nil
		for _, name := range slices.Sorted(maps.Keys(entries)) {
			if drop(name, entries[name]) {
				delete(cm.Data, name)
				removed = append(removed, name)
			}
		}
		return len(removed) > 0, nil
	})
	if err != nil {
		return nil, fmt.Errorf("prune the revoked nodes: %w", err)
	}
	return removed, nil
}

// update reads the ConfigMap, lets change change it, and writes it back if change reports a
// change, creating it if there was none; an error of change is returned. Another control plane
// may write it in between: the write then conflicts, and the update starts over from what that
// one wrote.
func update(ctx context.Context, cs kubernetes.Interface, change func(*corev1.ConfigMap, map[string]Entry) (bool, error)) error {
	retriable := func(err error) bool { return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) }
	return retry.OnError(retry.DefaultRetry, retriable, func() error {
		configMaps := cs.CoreV1().ConfigMaps(Namespace)
		cm, err := configMaps.Get(ctx, Name, metav1.GetOptions{})
		missing := apierrors.IsNotFound(err)
		if err != nil && !missing {
			return err
		}
		if missing {
			cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: Namespace, Name: Name}}
		}
		changed, err := change(cm, parse(cm))
		if err != nil {
			return err
		}
		switch {
		case !changed:
			return nil
		case missing:
			_, err = configMaps.Create(ctx, cm, metav1.CreateOptions{})
		default:
			_, err = configMaps.Update(ctx, cm, metav1.UpdateOptions{})
		}
		return err
	})
}
