package kubernetes

import (
	"strings"
	"testing"
	"time"

	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/kubernetes/kubetest"
	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
)

// Payloads issue no share to a node without a Node document, and a worker's share to a worker.
func TestPayloads(t *testing.T) {
	now := time.Now()
	secrets, err := clustersecrets.Generate(now)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := (Layer{}).Payloads(secrets, "w1", nil, now); err != nil || len(got) != 0 {
		t.Errorf("payloads of a node without a Node document = %v, %v", got, err)
	}
	docs, err := config.Parse(strings.NewReader(kubetest.NodeDoc("w1", "{type: worker}")))
	if err != nil {
		t.Fatal(err)
	}
	got, err := (Layer{}).Payloads(secrets, "w1", docs, now)
	if err != nil || len(got) != 1 || !kpki.IsPayload(got[0]) {
		t.Fatalf("payloads of a worker = %v, %v", got, err)
	}
	data, err := kpki.SharePayload(got[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kpki.ParseShare(data); err != nil {
		t.Errorf("the worker's share: %v", err)
	}
}
