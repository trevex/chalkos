package kubernetes

import (
	"fmt"
	"time"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/types/known/anypb"

	"github.com/trevex/chalkos/pkg/chalkctl/layer"
	"github.com/trevex/chalkos/pkg/internal/cli"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// Payloads are the node's share, issued from the secrets for its Node type; none for a node
// without one.
func (p *Plugin) Payloads(t layer.Target, now time.Time) ([]*anypb.Any, error) {
	kind, err := t.Cluster().Kind(t.Name())
	if err != nil || kind == "" {
		return nil, err
	}
	secrets := t.Secrets()
	share, err := kpki.ShareFor(&secrets, kind, t.Name(), now)
	if err != nil {
		return nil, err
	}
	data, err := share.Encode()
	if err != nil {
		return nil, err
	}
	payload, err := kpki.Payload(data)
	if err != nil {
		return nil, err
	}
	return []*anypb.Any{payload}, nil
}

// applyFlags are --kubernetes-share and its --force.
type applyFlags struct{ share, force bool }

// ApplyFlags registers --kubernetes-share and --force.
func (p *Plugin) ApplyFlags(fs *pflag.FlagSet) layer.ApplyFlags {
	f := &applyFlags{}
	fs.BoolVar(&f.share, "kubernetes-share", false, "also deliver a new Kubernetes share, such as a new kubelet certificate for a worker")
	fs.BoolVar(&f.force, "force", false, "with --kubernetes-share, deliver the share without asking the control planes whether the node is revoked")
	return f
}

func (f *applyFlags) Check(staged bool) error {
	switch {
	case staged && f.share:
		return cli.Usagef("--staged delivers config alone; deliver the Kubernetes share to the running image")
	case f.force && !f.share:
		return cli.Usagef("--force goes with --kubernetes-share")
	}
	return nil
}

func (f *applyFlags) Requested() bool { return f.share }
func (f *applyFlags) Admitted() bool  { return !f.force }
func (f *applyFlags) Does() string    { return "apply-config delivers it a Kubernetes share" }

func (f *applyFlags) None(name string) error {
	return fmt.Errorf("%s is no Kubernetes node: its Node names no type", name)
}
