package store

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// WriteRuntime writes a config set for the node's services. Its documents, Secrets excepted, go
// below runDir/config as <group>/<Kind>/<name>.json, readable by all. For each consumer, a unit
// by name with the kinds it reads, the values the secretRefs of those kinds' documents name go
// to runDir/credentials/<unit>/<secret>.<key>, readable by root alone, which the unit loads with
// LoadCredential=secret:<that directory>. Every consumer gets its directory, empty when it
// references no secret, because systemd refuses to start a unit whose credential path is
// missing. schemas tell which fields are secretRefs; nil when docs is empty.
func WriteRuntime(runDir string, docs []config.Document, schemas *schema.Schemas, consumers map[string][]string) error {
	creds, err := credentials(docs, schemas, consumers)
	if err != nil {
		return err
	}
	if err := replace(filepath.Join(runDir, "config"), func(tmp string) error {
		return writeDocuments(tmp, docs, false)
	}); err != nil {
		return err
	}
	root := filepath.Join(runDir, "credentials")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	for _, unit := range slices.Sorted(maps.Keys(creds)) {
		if err := syncFiles(filepath.Join(root, unit), creds[unit]); err != nil {
			return err
		}
	}
	return nil
}

// CheckCredentials refuses a set whose secretRefs could not all become credential files: a key
// that is no file name, or two references that would share a file name. It checks every
// reference in the set, whichever units read them, so it needs no consumer map.
func CheckCredentials(docs []config.Document, schemas *schema.Schemas) error {
	seen := map[string]config.SecretRef{}
	for _, d := range docs {
		for _, ref := range config.SecretRefs(d, schemas) {
			if err := checkCredential(ref); err != nil {
				return fmt.Errorf("%s: %w", d, err)
			}
			name := credentialFile(ref)
			if other, ok := seen[name]; ok && other != ref {
				return fmt.Errorf("%s: key %q of Secret %q and key %q of Secret %q would both be the credential file %s; rename one", d, ref.Key, ref.Name, other.Key, other.Name, name)
			}
			seen[name] = ref
		}
	}
	return nil
}

// credentials returns, by unit, the files of the values its kinds' secretRefs name.
func credentials(docs []config.Document, schemas *schema.Schemas, consumers map[string][]string) (map[string]map[string][]byte, error) {
	data := map[string]map[string]string{}
	for _, d := range docs {
		if d.IsSecret() {
			data[d.Name] = secretData(d)
		}
	}
	out := map[string]map[string][]byte{}
	for unit, kinds := range consumers {
		if !validName(unit) {
			return nil, fmt.Errorf("the consumer %q is no unit name", unit)
		}
		files := map[string][]byte{}
		for _, d := range docs {
			if d.IsSecret() || !slices.Contains(kinds, d.Kind) {
				continue
			}
			for _, ref := range config.SecretRefs(d, schemas) {
				if err := checkCredential(ref); err != nil {
					return nil, fmt.Errorf("%s: %w", d, err)
				}
				v, ok := data[ref.Name][ref.Key]
				if !ok {
					return nil, fmt.Errorf("%s names key %q of Secret %q, which the config does not hold", d, ref.Key, ref.Name)
				}
				files[credentialFile(ref)] = []byte(v)
			}
		}
		out[unit] = files
	}
	return out, nil
}

func credentialFile(ref config.SecretRef) string { return ref.Name + "." + ref.Key }

func checkCredential(ref config.SecretRef) error {
	if !validName(ref.Name) || !validName(ref.Key) || !validName(credentialFile(ref)) {
		return fmt.Errorf("key %q of Secret %q cannot name a credential file", ref.Key, ref.Name)
	}
	return nil
}

func secretData(d config.Document) map[string]string {
	spec, _ := d.Object["spec"].(map[string]any)
	data, _ := spec["data"].(map[string]any)
	out := make(map[string]string, len(data))
	for k, v := range data {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// syncFiles makes dir hold exactly files, each readable by root alone. A unit reads its
// credentials when it starts, after the restart that follows this, so files are replaced one by
// one rather than as a set.
func syncFiles(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if _, keep := files[e.Name()]; !keep {
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := writeFile(filepath.Join(dir, name), files[name], 0o600); err != nil {
			return err
		}
	}
	return nil
}
