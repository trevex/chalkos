package store

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/schema"
)

// WriteRuntime writes a config set for the node's services. Its documents go below runDir/config as
// <group>/<Kind>/<name>.json, readable by all, except Secrets and the documents that stay stored,
// such as Addons; replacing the directory removes any an earlier write left. For each granted
// unit, by name with the kinds it reads, the values the secretRefs of those kinds' documents
// name go to runDir/credentials/<unit>/<secret>.<key>, readable by root alone, which the unit
// loads with LoadCredential=secret:<that directory>. Every granted unit gets its directory, empty
// when it references no secret, because systemd refuses to start a unit whose credential path
// is missing. schemas tell which fields are secretRefs; nil when docs is empty.
func WriteRuntime(runDir string, docs []config.Document, schemas *schema.Schemas, units map[string][]string) error {
	creds, err := credentials(docs, schemas, units)
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

// ReadRuntime returns the documents WriteRuntime wrote below runDir/config, ordered by apiVersion,
// kind and name; none when there are none.
func ReadRuntime(runDir string) ([]config.Document, error) {
	root, err := filepath.EvalSymlinks(filepath.Join(runDir, "config"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return readDocuments(root)
}

// credentials returns, by unit, the files of the values its kinds' secretRefs name.
func credentials(docs []config.Document, schemas *schema.Schemas, units map[string][]string) (map[string]map[string][]byte, error) {
	data := map[string]map[string]string{}
	for _, d := range docs {
		if d.IsSecret() {
			data[d.Name] = secretData(d)
		}
	}
	out := map[string]map[string][]byte{}
	for unit, kinds := range units {
		if !validName(unit) {
			return nil, fmt.Errorf("the granted unit %q is no unit name", unit)
		}
		files := map[string][]byte{}
		for _, d := range docs {
			if d.IsSecret() || !slices.Contains(kinds, d.Kind) {
				continue
			}
			for _, ref := range config.SecretRefs(d, schemas) {
				if err := config.CheckCredential(ref); err != nil {
					return nil, fmt.Errorf("%s: %w", d, err)
				}
				v, ok := data[ref.Name][ref.Key]
				if !ok {
					return nil, fmt.Errorf("%s names key %q of Secret %q, which the config does not hold", d, ref.Key, ref.Name)
				}
				files[config.CredentialFile(ref)] = []byte(v)
			}
		}
		out[unit] = files
	}
	return out, nil
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
		if err := atomicfile.Replace(filepath.Join(dir, name), files[name], 0o600); err != nil {
			return err
		}
	}
	return nil
}

// CredentialSecrets returns the value a secretRef names among the credentials of a granted unit,
// as systemd loads them into dir, the unit's $CREDENTIALS_DIRECTORY, and whether dir holds it. A
// reference that could not name a credential file, such as one holding /, holds no value, so no
// reference reads a file outside dir.
func CredentialSecrets(dir string) func(config.SecretRef) (string, bool) {
	return func(ref config.SecretRef) (string, bool) {
		if dir == "" || config.CheckCredential(ref) != nil {
			return "", false
		}
		data, err := os.ReadFile(filepath.Join(dir, config.CredentialPrefix+"_"+config.CredentialFile(ref)))
		return string(data), err == nil
	}
}
