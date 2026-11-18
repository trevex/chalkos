package chalkctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/trevex/chalkos/pkg/clustersecrets"
)

type sopsSecretsFile struct {
	ctx        context.Context
	stderr     io.Writer
	format     string
	ciphertext []byte
	plaintext  []byte
}

func readSOPSSecretsFile(ctx context.Context, stderr io.Writer, path string, data []byte, change changeFlags) (*secretsFile, error) {
	format := "yaml"
	if json.Valid(data) {
		format = "json"
	}
	plain, err := runSOPS(ctx, stderr, data, "decrypt", "--input-type", format, "--output-type", format, "/dev/stdin")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	plain, err = clusterBundleJSON(plain)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	secrets, err := clustersecrets.ReadJSON(plain)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	f := &secretsFile{
		secrets: secrets, path: path, out: path,
		sops: &sopsSecretsFile{ctx: ctx, stderr: stderr, format: format, ciphertext: data, plaintext: plain},
	}
	return f, f.configurePublic(change)
}

// encode patches a private copy of the ciphertext. SOPS keeps the file's encryption
// settings and keys. The original is replaced by the caller only after every change succeeds.
func (f *sopsSecretsFile) encode(plain []byte) ([]byte, error) {
	before, err := sopsFields(f.plaintext)
	if err != nil {
		return nil, err
	}
	after, err := sopsFields(plain)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "chalkos-sops-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "secrets."+f.format)
	if err := os.WriteFile(path, f.ciphertext, 0o600); err != nil {
		return nil, err
	}
	for _, key := range slices.Sorted(maps.Keys(after)) {
		if bytes.Equal(before[key], after[key]) {
			continue
		}
		pointer := fmt.Sprintf("[%q]", key)
		if _, err := runSOPS(f.ctx, f.stderr, after[key], "set", "--value-stdin", "--input-type", f.format, "--output-type", f.format, path, pointer); err != nil {
			return nil, fmt.Errorf("update SOPS field %s: %w", key, err)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(before)) {
		if _, ok := after[key]; ok {
			continue
		}
		pointer := fmt.Sprintf("[%q]", key)
		if _, err := runSOPS(f.ctx, f.stderr, nil, "unset", "--input-type", f.format, "--output-type", f.format, path, pointer); err != nil {
			return nil, fmt.Errorf("remove SOPS field %s: %w", key, err)
		}
	}
	return os.ReadFile(path)
}

func sopsFields(plain []byte) (map[string][]byte, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(plain, &values); err != nil {
		return nil, err
	}
	fields := make(map[string][]byte, len(values))
	for key, value := range values {
		var compact bytes.Buffer
		if err := json.Compact(&compact, value); err != nil {
			return nil, err
		}
		fields[key] = compact.Bytes()
	}
	return fields, nil
}
