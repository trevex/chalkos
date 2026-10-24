package etcd

import (
	"bytes"
	"context"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// CountByKey counts the values below prefix by the key they are encrypted with, "" for those
// stored unencrypted. It reads them in pages, so no response holds them all, every page at the
// revision of the first, so writes meanwhile neither add to the count nor move a value past the
// next page's start.
func CountByKey(ctx context.Context, kv clientv3.KV, prefix string) (map[string]uint64, error) {
	counts := map[string]uint64{}
	end := clientv3.GetPrefixRangeEnd(prefix)
	from := prefix
	var rev int64
	for {
		opts := []clientv3.OpOption{clientv3.WithRange(end), clientv3.WithLimit(200)}
		if rev > 0 {
			opts = append(opts, clientv3.WithRev(rev))
		}
		resp, err := kv.Get(ctx, from, opts...)
		if err != nil {
			return nil, fmt.Errorf("read %s from etcd: %w", prefix, err)
		}
		if rev == 0 {
			rev = resp.Header.Revision
		}
		for _, kv := range resp.Kvs {
			counts[EncryptionKeyOf(kv.Value)]++
		}
		if !resp.More || len(resp.Kvs) == 0 {
			return counts, nil
		}
		from = string(resp.Kvs[len(resp.Kvs)-1].Key) + "\x00"
	}
}

// EncryptionKeyOf returns the name of the key a value etcd holds is encrypted with, "" for a
// value stored unencrypted. The API server writes it before the ciphertext:
// k8s:enc:<provider>:v1:<name>:.
func EncryptionKeyOf(value []byte) string {
	if !bytes.HasPrefix(value, []byte("k8s:enc:")) {
		return ""
	}
	fields := bytes.SplitN(value, []byte(":"), 6)
	if len(fields) < 6 {
		return ""
	}
	return string(fields[4])
}
