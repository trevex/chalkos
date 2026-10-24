package etcd

import (
	"context"
	"fmt"
	"testing"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/trevex/chalkos/pkg/kubernetes/etcd/etcdtest"
)

func TestEncryptionKeyOf(t *testing.T) {
	for value, want := range map[string]string{
		"k8s:enc:secretbox:v1:chalkos:data":    "chalkos",
		"k8s:enc:secretbox:v1:chalkos-2:a:b:c": "chalkos-2",
		"k8s\x00protobuf":                      "",
		`{"kind":"Secret"}`:                    "",
		"k8s:enc:short":                        "",
	} {
		if got := EncryptionKeyOf([]byte(value)); got != want {
			t.Errorf("%q: %q, want %q", value, got, want)
		}
	}
}

// pagedKV puts a key between the first and the second page of a read.
type pagedKV struct {
	clientv3.KV
	gets  int
	later func()
}

func (p *pagedKV) Get(ctx context.Context, key string, opts ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	if p.gets++; p.gets == 2 {
		p.later()
	}
	return p.KV.Get(ctx, key, opts...)
}

// TestCountByKeyReadsOneRevision checks that a count spanning pages reads them all at the
// revision of the first, so a key written meanwhile neither adds to it nor shifts it.
func TestCountByKeyReadsOneRevision(t *testing.T) {
	ca := etcdtest.NewCA(t)
	m := etcdtest.StartNew(t, ca, "cp0")
	cli, err := Dial([]string{m.ClientURL}, etcdtest.ClientTLS(t, ca))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	ctx := context.Background()
	for i := range 250 {
		if _, err := cli.Put(ctx, fmt.Sprintf("/registry/secrets/default/s-%03d", i), "k8s:enc:secretbox:v1:old:x"); err != nil {
			t.Fatal(err)
		}
	}
	kv := &pagedKV{KV: cli.KV, later: func() {
		if _, err := cli.Put(ctx, "/registry/secrets/default/zz-later", "k8s:enc:secretbox:v1:old:x"); err != nil {
			t.Fatal(err)
		}
	}}
	counts, err := CountByKey(ctx, kv, "/registry/secrets/")
	if err != nil {
		t.Fatal(err)
	}
	if kv.gets < 2 || counts["old"] != 250 {
		t.Errorf("%d reads counted %v, want the 250 keys of the first read's revision", kv.gets, counts)
	}
}
