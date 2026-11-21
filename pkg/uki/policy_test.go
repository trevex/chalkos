package uki

import (
	"bytes"
	"strings"
	"testing"

	"github.com/trevex/chalkos/pkg/uki/ukitest"
)

func TestPolicySlot(t *testing.T) {
	policy := []byte("policy_name=chalkos policy_version=0.4.0\n")
	data := ukitest.PolicyUKI(policy, 16384, strings.Repeat("ab", 32))
	off, size, ok, err := PolicySlot(bytes.NewReader(data))
	if err != nil || !ok || size != 16384 {
		t.Fatalf("PolicySlot = %d, %d, %v, %v", off, size, ok, err)
	}
	if !bytes.HasPrefix(data[off:off+size], policy) || data[off+int64(len(policy))] != 0 {
		t.Errorf("the slot holds %q", data[off:off+int64(len(policy))+1])
	}
}

func TestPolicySlotNone(t *testing.T) {
	for name, data := range map[string][]byte{
		"no initrd":             ukitest.UKI(map[string]string{"IMAGE_ID": "chalkos"}, "usrhash=00"),
		"initrd without policy": ukitest.Build(map[string][]byte{".initrd": ukitest.Newc(map[string][]byte{"init": []byte("x")})}),
	} {
		if _, _, ok, err := PolicySlot(bytes.NewReader(data)); ok || err != nil {
			t.Errorf("%s: ok %v, err %v", name, ok, err)
		}
	}
}

func TestPolicySlotRefusesBrokenArchive(t *testing.T) {
	data := ukitest.Build(map[string][]byte{".initrd": []byte("070701" + strings.Repeat("z", 104))})
	if _, _, _, err := PolicySlot(bytes.NewReader(data)); err == nil {
		t.Error("no error")
	}
}
