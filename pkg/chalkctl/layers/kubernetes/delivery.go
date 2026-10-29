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
	kind, err := kindOf(t.Cluster(), t.Name())
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

// applyFlags are --kubernetes-share.
type applyFlags struct{ share bool }

// ApplyFlags registers --kubernetes-share.
func (p *Plugin) ApplyFlags(fs *pflag.FlagSet) layer.ApplyFlags {
	f := &applyFlags{}
	fs.BoolVar(&f.share, "kubernetes-share", false, "also deliver a new Kubernetes share, such as a new kubelet certificate for a worker")
	return f
}

func (f *applyFlags) Check(staged bool) error {
	if staged && f.share {
		return cli.Usagef("--staged delivers config alone; deliver the Kubernetes share to the running image")
	}
	return nil
}

func (f *applyFlags) Flag() string    { return "--kubernetes-share" }
func (f *applyFlags) Requested() bool { return f.share }
func (f *applyFlags) Does() string    { return "apply-config delivers it a Kubernetes share" }

func (f *applyFlags) None(name string) error {
	return fmt.Errorf("%s is no Kubernetes node: its Node names no type", name)
}
