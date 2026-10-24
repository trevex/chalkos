package clustersecrets

import (
	"regexp"
	"strings"
	"testing"
)

func TestRecoveryKey(t *testing.T) {
	secret := []byte(strings.Repeat("s", RecoverySecretSize))
	key, err := RecoveryKey(secret, "w1")
	if err != nil {
		t.Fatal(err)
	}
	// The shape of `systemd-cryptenroll --recovery-key` output.
	if !regexp.MustCompile(`^([cbdefghijklnrtuv]{8}-){7}[cbdefghijklnrtuv]{8}$`).MatchString(key) {
		t.Errorf("recovery key %q is not formatted like a systemd recovery key", key)
	}
	if want := "kekkkver-neluervv-ndcbbvht-ujthgtir-lnjnfket-blnruebv-nrdtbjig-nhefnrbg"; key != want {
		t.Errorf("recovery key = %q, want %q", key, want)
	}
	again, _ := RecoveryKey(secret, "w1")
	other, _ := RecoveryKey(secret, "w2")
	if again != key || other == key {
		t.Error("recovery keys must be stable per node and differ between nodes")
	}
	if _, err := RecoveryKey(secret[:16], "w1"); err == nil {
		t.Error("accepted a short secret")
	}
	if _, err := RecoveryKey(secret, "a\x00b"); err == nil || !strings.Contains(err.Error(), "node name") {
		t.Errorf("err = %v, want a node name containing a NUL byte refused", err)
	}
}
