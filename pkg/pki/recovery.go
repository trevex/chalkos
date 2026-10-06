package pki

import (
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"strings"
)

// RecoverySecretSize is the size of the secret recovery keys are derived from.
const RecoverySecretSize = 32

// modhex is the alphabet systemd uses for recovery keys; it types the same on most keyboard
// layouts.
const modhex = "cbdefghijklnrtuv"

// RecoveryKey derives a node's recovery key from the cluster's recovery secret, so the secrets
// file never has to be rewritten when a node is added. It is formatted like the keys
// systemd-cryptenroll --recovery-key generates: 32 bytes as 8 dash-separated groups of 8
// modhex characters.
func RecoveryKey(secret []byte, cluster, node string) (string, error) {
	if len(secret) != RecoverySecretSize {
		return "", errors.New("the recovery secret must be 32 bytes")
	}
	// The NUL separator keeps the info unambiguous between cluster and node names, so reject one
	// that contains a NUL byte rather than let two different names collide.
	if strings.Contains(cluster, "\x00") || strings.Contains(node, "\x00") {
		return "", errors.New("cluster and node names must not contain a NUL byte")
	}
	key, err := hkdf.Key(sha256.New, secret, nil, "chalkos recovery key\x00"+cluster+"\x00"+node, 32)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for i, c := range key {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(modhex[c>>4])
		b.WriteByte(modhex[c&0xf])
	}
	return b.String(), nil
}
