// Package revoked keeps the cluster's revoked nodes in a ConfigMap: the names of nodes that are
// cut off, with the time each was revoked. Control planes read it before they renew a node
// certificate, and the admission policy reads it as its parameters.
package revoked

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
)

// The ConfigMap's data maps each revoked node's name to its revocation time in TimeFormat.
const (
	Namespace  = "kube-system"
	Name       = "chalkos-revoked-nodes"
	TimeFormat = time.RFC3339
)

// List maps the names of revoked nodes to the times they were revoked.
type List map[string]time.Time

// Load reads the list; a cluster without the ConfigMap has revoked no node.
func Load(ctx context.Context, cs kubernetes.Interface) (List, error) {
	cm, err := cs.CoreV1().ConfigMaps(Namespace).Get(ctx, Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return List{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the revoked nodes: %w", err)
	}
	return parse(cm)
}

func parse(cm *corev1.ConfigMap) (List, error) {
	l := List{}
	for name, value := range cm.Data {
		at, err := time.Parse(TimeFormat, value)
		if err != nil {
			return nil, fmt.Errorf("the revoked nodes: %s: %w", name, err)
		}
		l[name] = at
	}
	return l, nil
}

// Revoke lists the node as revoked at now and returns the time it is listed with: that of an
// earlier revocation, if there was one.
func Revoke(ctx context.Context, cs kubernetes.Interface, name string, now time.Time) (time.Time, error) {
	var at time.Time
	err := update(ctx, cs, func(cm *corev1.ConfigMap, l List) bool {
		if earlier, ok := l[name]; ok {
			at = earlier
			return false
		}
		at = now.UTC().Truncate(time.Second)
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		cm.Data[name] = at.Format(TimeFormat)
		return true
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("revoke %s: %w", name, err)
	}
	return at, nil
}

// Unrevoke removes the node from the list. It returns the time the node was revoked and whether it
// was listed at all.
func Unrevoke(ctx context.Context, cs kubernetes.Interface, name string) (time.Time, bool, error) {
	var at time.Time
	var listed bool
	err := update(ctx, cs, func(cm *corev1.ConfigMap, l List) bool {
		at, listed = l[name]
		if listed {
			delete(cm.Data, name)
		}
		return listed
	})
	if err != nil {
		return time.Time{}, false, fmt.Errorf("unrevoke %s: %w", name, err)
	}
	return at, listed, nil
}

// update reads the ConfigMap, lets change change it, and writes it back if change reports a
// change, creating it if there was none. Another control plane may write it in between: the
// write then conflicts, and the update starts over from what that one wrote.
func update(ctx context.Context, cs kubernetes.Interface, change func(*corev1.ConfigMap, List) bool) error {
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
		l, err := parse(cm)
		if err != nil {
			return err
		}
		switch {
		case !change(cm, l):
			return nil
		case missing:
			_, err = configMaps.Create(ctx, cm, metav1.CreateOptions{})
		default:
			_, err = configMaps.Update(ctx, cm, metav1.UpdateOptions{})
		}
		return err
	})
}
