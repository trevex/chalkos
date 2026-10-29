package layer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	nodev1 "github.com/trevex/chalkos/pkg/api/node/v1"
	"github.com/trevex/chalkos/pkg/client"
	"github.com/trevex/chalkos/pkg/clustersecrets"
)

// Rotation runs kinds of rotation of chalkctl rotate. chalkctl runs the rotations of the CAs
// every node takes part in itself, and asks every compiled plugin for its kinds.
type Rotation interface {
	// Kinds are the rotations it runs, in the order chalkctl names them.
	Kinds() []clustersecrets.RotationKind
	// CheckStart refuses to start a rotation of kind, as on a cluster without the nodes it runs
	// through. It runs before the secrets file records the rotation.
	CheckStart(ctx context.Context, r Rotating, kind clustersecrets.RotationKind) error
	// ApplyRotation delivers a phase of the rotation of kind to the nodes and waits until each
	// confirms it.
	ApplyRotation(ctx context.Context, r Rotating, kind clustersecrets.RotationKind, phase clustersecrets.Phase) error
	// PausesAfter reports whether the rotation of kind stops for the operator once every node
	// applied the phase. A kind that does not pause after the refresh finishes without --finish.
	PausesAfter(kind clustersecrets.RotationKind, phase clustersecrets.Phase) bool
	// Paused says what the operator does before the rotation continues.
	Paused(ctx context.Context, r Rotating, rot clustersecrets.Rotation) error
	// FinishGuard refuses to remove an old value of kind something still needs; force is --force.
	FinishGuard(ctx context.Context, r Rotating, kind clustersecrets.RotationKind, force bool) error
	// Finished says what the end of the rotation of kind leaves: the state of the nodes, which
	// follows "the rotation of the <kind> is finished: ", and advice for what the finish cannot
	// reach, such as clients' files to issue again; advice is "" without any.
	Finished(kind clustersecrets.RotationKind) (state, advice string)
}

// RotationNodes is a plugin whose layer names the nodes every rotation leaves out and those it
// delivers the node CA to. chalkctl asks every compiled plugin.
type RotationNodes interface {
	// RevokedNodes are the nodes the layer lists as revoked, each with when it was, as
	// "since <time>". A rotation hands them nothing.
	RevokedNodes(ctx context.Context, r Rotating) (map[string]string, error)
	// NodeCAHolders are the nodes that hold the node CA and renew node certificates with it, in
	// name order.
	NodeCAHolders(c Cluster) ([]string, error)
}

// Rotating is a run of chalkctl rotate, as a plugin's rotations use it.
type Rotating interface {
	// Say prints a line of the run's progress.
	Say(format string, args ...any)
	// Secrets are the secrets file's as the rotation has them now.
	Secrets() clustersecrets.Secrets
	// Now is the time the run issues at.
	Now() time.Time
	// Timeout bounds each wait for a node.
	Timeout() time.Duration
	// Poll is the time between two looks at what the run waits for.
	Poll() time.Duration
	// Cluster is the cluster the run rotates.
	Cluster() Cluster
	// NodeNames lists the nodes that take part in name order: the cluster's, the revoked ones
	// left out.
	NodeNames() []string
	// Call runs fn on a connection to the installed node.
	Call(name string, fn func(conn *client.Conn) error) error
	// Status reads a node's status.
	Status(ctx context.Context, name string) (*nodev1.StatusResponse, error)
	// DeliverPayloads sends a node the payloads its layers issue it from the secrets as they are
	// now.
	DeliverPayloads(ctx context.Context, name string) error
	// Nodes reaches the cluster's nodes with the secrets as they are now.
	Nodes() (Nodes, error)
}

// WaitStatus waits, up to the run's timeout, until a node's status passes check.
func WaitStatus(ctx context.Context, r Rotating, name, what string, check func(st *nodev1.StatusResponse) error) error {
	return WaitFor(ctx, name+" "+what, r.Timeout(), r.Poll(), func() error {
		st, err := r.Status(ctx, name)
		if err != nil {
			return err
		}
		return check(st)
	})
}

// EachNode runs fn for every node, also after one failed, and names those that failed.
func EachNode(names []string, fn func(name string) error) error {
	var failed []string
	for _, name := range names {
		if err := fn(name); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failed) > 0 {
		return errors.New(strings.Join(failed, "; "))
	}
	return nil
}
