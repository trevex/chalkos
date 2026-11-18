package chalkctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"filippo.io/age"
	"go.yaml.in/yaml/v3"

	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/internal/strs"
	"github.com/trevex/chalkos/pkg/inventory"
	"github.com/trevex/chalkos/pkg/schema"
)

type secretSource struct {
	provider string
	path     string
	pointer  string
	// inferred tells that the provider comes from the file name, not from a provider URI.
	inferred bool
}

func clusterSecretsSource(input string) (secretSource, error) {
	source, err := parseSecretSource(input, "", false)
	if err == nil && source.pointer != "" {
		return secretSource{}, errors.New("--secrets requires a dedicated cluster secrets file; selectors are only for Secret documents")
	}
	return source, err
}

var providerPrefix = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)

func parseSecretSource(input, base string, requireURI bool) (secretSource, error) {
	var source secretSource
	if prefix := providerPrefix.FindString(input); prefix != "" {
		source.provider = strings.ToLower(strings.TrimSuffix(prefix, "://"))
		switch source.provider {
		case "file", "sops", "age":
		default:
			return secretSource{}, fmt.Errorf("unknown secrets provider %q", source.provider)
		}
		rawPath, fragment, _ := strings.Cut(input[len(prefix):], "#")
		path, err := url.PathUnescape(rawPath)
		if err != nil || path == "" || strings.Contains(rawPath, "?") || strings.ContainsRune(path, 0) {
			return secretSource{}, errors.New("invalid local path in secrets source URI")
		}
		pointer, err := url.PathUnescape(fragment)
		if err != nil || (pointer != "" && !strings.HasPrefix(pointer, "/")) {
			return secretSource{}, errors.New("the fragment in a secrets source URI must be a JSON Pointer")
		}
		source.path, source.pointer = path, pointer
	} else {
		if requireURI {
			return secretSource{}, errors.New("a Secret source must use a provider URI such as file://, sops:// or age://")
		}
		if input == "-" {
			return secretSource{path: "-"}, nil
		}
		source.path, source.inferred = input, true
		switch name := filepath.Base(input); {
		case strings.HasSuffix(name, ".age"):
			source.provider = "age"
		case strings.Contains(name, ".enc.") && !strings.HasSuffix(name, ".enc."):
			source.provider = "sops"
		default:
			source.provider = "file"
		}
	}
	if !filepath.IsAbs(source.path) {
		source.path = filepath.Join(base, source.path)
	}
	path, err := filepath.Abs(source.path)
	if err != nil {
		return secretSource{}, err
	}
	source.path = path
	return source, nil
}

// secretSources keeps file reads and decryptions consistent across nodes and consumers in one command.
type secretSources struct {
	ctx        context.Context
	app        *app
	identities []string
	mu         sync.Mutex
	files      map[secretSource][]byte
}

type secretSourcesKey struct{}

func sourcesIn(ctx context.Context) *secretSources {
	sources, _ := ctx.Value(secretSourcesKey{}).(*secretSources)
	return sources
}

func (s *secretSources) read(source secretSource) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := secretSource{provider: source.provider, path: source.path, inferred: source.inferred}
	if data, ok := s.files[key]; ok {
		return data, nil
	}
	var data []byte
	var err error
	if source.provider == "sops" {
		data, err = runSOPS(s.ctx, s.app.stderr, nil, "decrypt", source.path)
		if err != nil {
			return nil, err
		}
	} else {
		data, err = os.ReadFile(source.path)
		if err != nil {
			return nil, err
		}
		format, _ := clustersecrets.DetectFormat(data)
		encrypted := format == clustersecrets.FormatAge || format == clustersecrets.FormatArmoredAge
		switch {
		case source.provider == "file" && encrypted && !source.inferred:
			return nil, errors.New("the file is age-encrypted; name it with age://")
		case source.provider == "age" || encrypted:
			// A file named on the command line is read as what it holds, whatever its name.
			data, err = clustersecrets.DecryptAge(data, func() ([]age.Identity, error) {
				return s.app.ageIdentities(s.ctx, s.identities)
			})
			if err != nil {
				return nil, err
			}
		}
	}
	if s.files == nil {
		s.files = make(map[secretSource][]byte)
	}
	s.files[key] = data
	return data, nil
}

// runSOPS runs sops with input on its standard input and returns its standard output, which can
// hold plaintext. sops's own messages, such as why it could not decrypt, go to stderr.
func runSOPS(ctx context.Context, stderr io.Writer, input []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sops", args...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	cmd.Stderr = stderr
	data, err := cmd.Output()
	if errors.Is(err, exec.ErrNotFound) {
		return nil, errors.New("the sops provider requires sops on PATH")
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("sops %s failed: %w; its messages are above", args[0], err)
	}
	return data, nil
}

func selectSecret(data []byte, pointer string) (*yaml.Node, error) {
	var doc, extra yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if dec.Decode(&doc) != nil || len(doc.Content) != 1 || dec.Decode(&extra) != io.EOF {
		return nil, errors.New("a secret selector requires a single YAML or JSON document")
	}
	node := doc.Content[0]
	if pointer == "" {
		return node, nil
	}
	for _, part := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		for index := 0; index < len(part); index++ {
			if part[index] == '~' {
				index++
				if index == len(part) || (part[index] != '0' && part[index] != '1') {
					return nil, errors.New("invalid JSON Pointer escape")
				}
			}
		}
		key := strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		var next *yaml.Node
		switch node.Kind {
		case yaml.MappingNode:
			for index := 0; index < len(node.Content); index += 2 {
				k := node.Content[index]
				if k.Tag == "!!str" && k.Value == key {
					if next != nil {
						return nil, errors.New("secret selector matches a duplicate key")
					}
					next = node.Content[index+1]
				}
			}
		case yaml.SequenceNode:
			index, err := strconv.Atoi(key)
			if err == nil && index >= 0 && index < len(node.Content) && strconv.Itoa(index) == key {
				next = node.Content[index]
			}
		}
		if next == nil {
			return nil, errors.New("secret selector does not match a value")
		}
		node = next
	}
	return node, nil
}

func (s *secretSources) secretValue(value, base string) (string, error) {
	source, err := parseSecretSource(value, base, true)
	if err != nil {
		return "", err
	}
	data, err := s.read(source)
	if err != nil {
		return "", err
	}
	if source.pointer == "" {
		if !utf8.Valid(data) {
			return "", errors.New("the secret source is not UTF-8 text")
		}
		return string(data), nil
	}
	node, err := selectSecret(data, source.pointer)
	if err != nil {
		return "", err
	}
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" {
		return "", errors.New("the secret selector must resolve to a string")
	}
	return node.Value, nil
}

func (s *secretSources) bundle(source secretSource) (clustersecrets.Secrets, error) {
	data, err := s.read(source)
	if err != nil {
		return clustersecrets.Secrets{}, err
	}
	data, err = clusterBundleJSON(data)
	if err != nil {
		return clustersecrets.Secrets{}, err
	}
	return clustersecrets.ReadJSON(data)
}

func clusterBundleJSON(data []byte) ([]byte, error) {
	if json.Valid(data) {
		return data, nil
	}
	node, err := selectSecret(data, "")
	if err != nil {
		return nil, err
	}
	if node.Kind != yaml.MappingNode {
		return nil, errors.New("the cluster bundle must be an object")
	}
	var value map[string]any
	if node.Decode(&value) != nil {
		return nil, errors.New("cannot decode the cluster bundle")
	}
	data, err = json.Marshal(value)
	if err != nil {
		return nil, errors.New("cannot decode the cluster bundle")
	}
	return data, nil
}

// materialize resolves the sources of nc's Secret documents into their spec.data, decrypting
// those of sops and age.
func (s *secretSources) materialize(nc *inventory.NodeConfig) (*inventory.NodeConfig, error) {
	resolved, _, err := s.resolve(nc, false)
	return resolved, err
}

// resolve resolves the sources of nc's Secret documents into their spec.data. With placeholders it
// decrypts nothing: a key from a sops or age source gets the source's URI as its value, and
// encrypted names the keys of each Secret that hold one, as "Secret bgp keys other and password".
func (s *secretSources) resolve(nc *inventory.NodeConfig, placeholders bool) (_ *inventory.NodeConfig, encrypted []string, _ error) {
	resolved := *nc
	resolved.Documents = slices.Clone(nc.Documents)
	for i, doc := range resolved.Documents {
		if !doc.IsSecret() {
			continue
		}
		spec, _ := doc.Object["spec"].(map[string]any)
		rawSources, present := spec["sources"]
		if !present {
			continue
		}
		if s == nil {
			return nil, nil, errors.New("Secret sources require a chalkctl command")
		}
		filename := nc.DocumentPath(doc.ID())
		where := fmt.Sprintf("%s: Secret %q", filename, doc.Name)
		sources, ok := rawSources.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("%s: spec.sources must map keys to provider URIs", where)
		}
		doc.Object = schema.DeepCopy(doc.Object).(map[string]any)
		spec = doc.Object["spec"].(map[string]any)
		data, ok := spec["data"].(map[string]any)
		if !ok {
			if _, present := spec["data"]; present {
				return nil, nil, fmt.Errorf("%s: spec.data must map keys to strings", where)
			}
			data = make(map[string]any)
		}
		var placeheld []string
		for _, key := range slices.Sorted(maps.Keys(sources)) {
			if _, present := data[key]; present {
				return nil, nil, fmt.Errorf("%s: key %q is defined in both spec.data and spec.sources", where, key)
			}
			value, ok := sources[key].(string)
			if !ok {
				return nil, nil, fmt.Errorf("%s: source for key %q must be a provider URI", where, key)
			}
			if placeholders {
				if source, err := parseSecretSource(value, "", true); err == nil && source.provider != "file" {
					data[key] = value
					placeheld = append(placeheld, key)
					continue
				}
			}
			resolvedValue, err := s.secretValue(value, filepath.Dir(filename))
			if err != nil {
				return nil, nil, fmt.Errorf("%s: resolve key %q: %w", where, key, err)
			}
			data[key] = resolvedValue
		}
		switch len(placeheld) {
		case 0:
		case 1:
			encrypted = append(encrypted, fmt.Sprintf("Secret %s key %s", doc.Name, placeheld[0]))
		default:
			encrypted = append(encrypted, fmt.Sprintf("Secret %s keys %s", doc.Name, strs.And(placeheld)))
		}
		spec["data"] = data
		delete(spec, "sources")
		resolved.Documents[i] = doc
	}
	return &resolved, encrypted, nil
}
