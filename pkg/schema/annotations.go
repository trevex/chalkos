package schema

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/trevex/chalkos/pkg/jsonfile"
	"github.com/trevex/chalkos/pkg/networkdschema"
)

// The x-chalkos-* keywords a kind's schema may hold, which lib/kinds/schema.nix and
// pkg/networkdschema write. compile decodes each of them once, strictly, into the Annotations of
// the schema node that holds it, and refuses a schema with a malformed one or with any other
// x-chalkos-* keyword, so a fault in an image's schemas fails the image's own load and never a
// delivery.
const (
	// kindKeyword is the root's record of the kind: {group, version, kind, multiple, withheld,
	// rules}.
	kindKeyword = "x-chalkos-kind"
	// refKeyword marks a field that names a document: {apiVersion, kind, reserved, key}.
	refKeyword = "x-chalkos-ref"
	// uniqueKeyword lists the values of a field, or of a kind's spec, that must not repeat.
	uniqueKeyword = "x-chalkos-unique"
	// rulesKeyword lists a field's change rules.
	rulesKeyword = "x-chalkos-rules"
	// kubernetesObjectKeyword marks an embedded Kubernetes object, whose fields are its own.
	kubernetesObjectKeyword = "x-chalkos-kubernetes-object"
	// networkdSectionKeyword and networkdKeyKeyword are a networkd unit's section and key as
	// systemd spells them, on the spec field of the section and on the section's field of the key.
	networkdSectionKeyword = networkdschema.SectionKeyword
	networkdKeyKeyword     = networkdschema.KeyKeyword
)

// Annotations are the x-chalkos-* keywords of a schema node, decoded. The zero value is a node
// without any.
type Annotations struct {
	// KindRules are the rules of the kind over its documents, from the root's x-chalkos-kind; only
	// a kind's root has them.
	KindRules []Rule
	// Ref is the document a field names, from x-chalkos-ref.
	Ref *Ref
	// Unique are the declarations of x-chalkos-unique.
	Unique []Unique
	// Rules are the field's change rules, from x-chalkos-rules.
	Rules []Rule
	// KubernetesObject tells that the node is an embedded Kubernetes object.
	KubernetesObject bool
	// NetworkdSection and NetworkdKey are the systemd names of a networkd section and key.
	NetworkdSection, NetworkdKey string
}

// Ref is a field's x-chalkos-ref: the apiVersion and kind of the documents it names, the names it
// reserves, which name no document, and whether it names a key of a Secret, as a secretRef does.
type Ref struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Reserved   []string `json:"reserved"`
	Key        bool     `json:"key"`
}

// Unique is one declaration of x-chalkos-unique, as the converter writes it from
// chalkosLib.mkOption's or chalkos.kinds.<Kind>'s unique: the values that must not repeat within
// one list (across "list") or among all documents of the kind (across "kind"). Without By the key
// is the value itself, or each item of a list, normalised with As; with By it is the tuple of
// those fields of the object, or of each object of a list, each normalised with its own As.
type Unique struct {
	Across string     `json:"across"`
	By     []KeyField `json:"by"`
	As     string     `json:"as"`
	Hint   string     `json:"hint"`
}

// KeyField is a member of a uniqueness declaration's tuple: a field of the object and its
// normaliser.
type KeyField struct {
	Field string `json:"field"`
	As    string `json:"as"`
}

// Normalisers are the names of the normalisers a uniqueness declaration may compare its values
// with, which pkg/config implements.
var Normalisers = []string{"cidr", "host", "ip", "lower"}

// Takes is what a change a rule refuses needs instead.
type Takes string

const (
	// Refuse: the change cannot be made; the message says what to do instead.
	Refuse Takes = "refuse"
	// Reset: chalkctl storage reset carries the change out.
	Reset Takes = "reset"
	// Reinstall: only a reinstall of the node carries the change out.
	Reinstall Takes = "reinstall"
)

// Rule is a change rule as a schema declares it: in a field's x-chalkos-rules, or in the rules of
// its kind's x-chalkos-kind.
type Rule struct {
	// Rule is a CEL expression that is true for every change the image accepts.
	Rule string `json:"rule"`
	// Message says why a change is refused and what to do instead; MessageExpression is CEL that
	// yields it, and the message is used when the expression fails.
	Message           string `json:"message,omitempty"`
	MessageExpression string `json:"messageExpression,omitempty"`
	// Takes is Refuse where the schema leaves it out.
	Takes Takes `json:"takes,omitempty"`
	// Description says what the rule guards, for the reference pages; the rule never reads or
	// says it.
	Description string `json:"description,omitempty"`
}

// kindDecl is a root's x-chalkos-kind.
type kindDecl struct {
	Group    string `json:"group"`
	Version  string `json:"version"`
	Kind     string `json:"kind"`
	Multiple bool   `json:"multiple"`
	Withheld bool   `json:"withheld"`
	Rules    []Rule `json:"rules"`
}

// ext is what compile attaches to every node of a compiled schema: its annotations and its
// keywords as its file writes them.
type ext struct {
	ann      Annotations
	keywords map[string]any
}

// Validate validates nothing: the annotations say nothing about a document's validity.
func (*ext) Validate(*jsonschema.ValidatorContext, any) {}

// extOf is the ext compile attached to s, or nil for a schema no Schemas compiled.
func extOf(s *jsonschema.Schema) *ext {
	for _, e := range s.Extensions {
		if x, ok := e.(*ext); ok {
			return x
		}
	}
	return nil
}

// AnnotationsOf returns the annotations of the compiled schema node s, $ref followed: compile
// refuses an annotation beside $ref, so a node with $ref has those of the schema it refers to.
// It is the zero Annotations for a node without any.
func AnnotationsOf(s *jsonschema.Schema) Annotations {
	if x := extOf(Resolve(s)); x != nil {
		return x.ann
	}
	return Annotations{}
}

// KeywordsOf returns the keywords of the compiled schema node s as its file writes them, numbers
// as json.Number, $ref followed: the keywords of the schema it refers to, with those beside each
// $ref, such as a description of its own, laid over them, and $ref itself left out. It is nil for
// a schema no Schemas compiled. The caller must not change them.
func KeywordsOf(s *jsonschema.Schema) map[string]any {
	x := extOf(s)
	if x == nil {
		return nil
	}
	if s.Ref == nil {
		return x.keywords
	}
	merged := maps.Clone(KeywordsOf(s.Ref))
	if merged == nil {
		merged = map[string]any{}
	}
	for k, v := range x.keywords {
		if k != "$ref" {
			merged[k] = v
		}
	}
	return merged
}

// vocabulary is the x-chalkos-* keywords as a vocabulary of the compiler, which decodes them on
// each schema object it compiles. roots are the root objects of the schema files, which alone may
// hold x-chalkos-kind; compile decodes it itself, before it compiles the file.
func vocabulary(roots map[uintptr]bool) *jsonschema.Vocabulary {
	return &jsonschema.Vocabulary{
		URL: "https://chalkos.dev/vocab/annotations",
		Compile: func(_ *jsonschema.CompilerContext, obj map[string]any) (jsonschema.SchemaExt, error) {
			ann, err := decodeAnnotations(obj, roots[mapID(obj)])
			if err != nil {
				return nil, &annotationError{obj: obj, err: err}
			}
			return &ext{ann: ann, keywords: obj}, nil
		},
	}
}

func mapID(m map[string]any) uintptr { return uintptr(reflect.ValueOf(m).UnsafePointer()) }

// decodeAnnotations decodes the x-chalkos-* keywords of a schema object; root tells whether it is a
// file's root, whose x-chalkos-kind kindOf decodes.
func decodeAnnotations(obj map[string]any, root bool) (Annotations, error) {
	var a Annotations
	var errs []error
	_, hasRef := obj["$ref"]
	for _, name := range slices.Sorted(maps.Keys(obj)) {
		if !strings.HasPrefix(name, "x-chalkos-") {
			continue
		}
		// A node with $ref has the annotations of the schema it refers to alone.
		if hasRef {
			errs = append(errs, fmt.Errorf("%s beside $ref: a node with $ref has the annotations of the schema it refers to, so the keyword belongs there", name))
			continue
		}
		v := obj[name]
		var err error
		switch name {
		case kindKeyword:
			if !root {
				err = errors.New("only a schema's root declares its kind")
			}
		case refKeyword:
			a.Ref, err = decodeRef(v)
		case uniqueKeyword:
			a.Unique, err = decodeUnique(v)
		case rulesKeyword:
			a.Rules, err = decodeRules(v)
		case kubernetesObjectKeyword:
			if v != true {
				err = errors.New("is not true")
			}
			a.KubernetesObject = true
		case networkdSectionKeyword:
			a.NetworkdSection, err = nonEmptyString(v)
		case networkdKeyKeyword:
			a.NetworkdKey, err = nonEmptyString(v)
		default:
			err = errors.New("is no keyword of chalkos's")
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return a, errors.Join(errs...)
}

// decodeStrict decodes a keyword's value into out, refusing keys out does not have.
func decodeStrict(v any, out any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return jsonfile.DecodeStrict(data, out)
}

func decodeRef(v any) (*Ref, error) {
	var r Ref
	if err := decodeStrict(v, &r); err != nil {
		return nil, err
	}
	if r.APIVersion == "" || r.Kind == "" {
		return nil, errors.New("names no apiVersion or no kind")
	}
	return &r, nil
}

// decodeUnique refuses keys a declaration does not have, a scope other than kind and list, and a
// normaliser other than Normalisers.
func decodeUnique(v any) ([]Unique, error) {
	var decls []Unique
	if err := decodeStrict(v, &decls); err != nil {
		return nil, err
	}
	for _, d := range decls {
		if d.Across != "kind" && d.Across != "list" {
			return nil, fmt.Errorf("across %q is neither kind nor list", d.Across)
		}
		as := []string{d.As}
		for _, f := range d.By {
			if f.Field == "" {
				return nil, errors.New("a member of by names no field")
			}
			as = append(as, f.As)
		}
		for _, n := range as {
			if n != "" && !slices.Contains(Normalisers, n) {
				return nil, fmt.Errorf("the normaliser %q is none of %s", n, strings.Join(Normalisers, ", "))
			}
		}
	}
	return decls, nil
}

// decodeRules refuses keys a rule does not have and a takes other than refuse, reset and
// reinstall, which it fills in as refuse where a rule leaves it out.
func decodeRules(v any) ([]Rule, error) {
	var rules []Rule
	if err := decodeStrict(v, &rules); err != nil {
		return nil, err
	}
	return rules, takesOf(rules)
}

// takesOf checks the takes of each of rules, filling in Refuse where a rule leaves it out.
func takesOf(rules []Rule) error {
	for i := range rules {
		switch rules[i].Takes {
		case "":
			rules[i].Takes = Refuse
		case Refuse, Reset, Reinstall:
		default:
			return fmt.Errorf("takes %q is none of refuse, reset and reinstall", rules[i].Takes)
		}
	}
	return nil
}

func nonEmptyString(v any) (string, error) {
	s, _ := v.(string)
	if s == "" {
		return "", errors.New("is not a non-empty string")
	}
	return s, nil
}

// annotationError is a malformed annotation of the schema object obj.
type annotationError struct {
	obj map[string]any
	err error
}

func (e *annotationError) Error() string { return e.err.Error() }

// locate says where in doc the schema object of an annotationError err is, as a JSON pointer;
// any other error it returns as it is.
func locate(err error, doc any) error {
	ae, ok := errors.AsType[*annotationError](err)
	if !ok {
		return err
	}
	at, ok := pointerTo(doc, mapID(ae.obj), "")
	if !ok {
		at = "an unknown location"
	}
	return fmt.Errorf("at '%s': %w", cmp.Or(at, "/"), ae.err)
}

// pointerTo finds the object of the given identity in v, at the JSON pointer at.
func pointerTo(v any, id uintptr, at string) (string, bool) {
	switch v := v.(type) {
	case map[string]any:
		if mapID(v) == id {
			return at, true
		}
		for _, k := range slices.Sorted(maps.Keys(v)) {
			esc := strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
			if p, ok := pointerTo(v[k], id, at+"/"+esc); ok {
				return p, true
			}
		}
	case []any:
		for i, item := range v {
			if p, ok := pointerTo(item, id, at+"/"+strconv.Itoa(i)); ok {
				return p, true
			}
		}
	}
	return "", false
}
