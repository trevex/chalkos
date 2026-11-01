package imagecheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/rules"
)

// fakeFacts answers each fact with its value or err, and records the facts asked and the input of
// each ask.
type fakeFacts struct {
	values map[string]any
	err    error
	asked  []string
	inputs []map[string]any
}

func (f *fakeFacts) Fact(name string, input map[string]any) (any, error) {
	f.asked = append(f.asked, name)
	f.inputs = append(f.inputs, input)
	return f.values[name], f.err
}

// serve serves f on a socket of its own and returns the facts a checker reads from it.
func serve(t *testing.T, f Facts) *RemoteFacts {
	t.Helper()
	path := filepath.Join(t.TempDir(), "facts.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ServeFacts(l, f))
	return DialFacts(context.Background(), path)
}

const doc = `{"apiVersion":"demo.example.org/v1alpha1","kind":"Demo","metadata":{"name":"one"},"spec":{"name":"first","replicas":12345678901234567890}}`

// The documents pass as JSON objects, their numbers exact.
func TestEncodeDecode(t *testing.T) {
	docs, err := Decode([]string{doc})
	if err != nil {
		t.Fatal(err)
	}
	out, err := Encode(docs)
	if err != nil || !slices.Equal(out, []string{doc}) {
		t.Errorf("Encode = %v, %v", out, err)
	}
	if _, err := Decode([]string{doc, `[1]`}); err == nil || !strings.HasPrefix(err.Error(), "document 2: ") {
		t.Errorf("Decode of no object = %v", err)
	}
}

// A checker's facts come from chalkd over the socket: the value with its numbers exact, the input
// command facts read, each fact asked once for one input, and an error as its message alone.
func TestFactsOverTheSocket(t *testing.T) {
	f := &fakeFacts{values: map[string]any{"recorded": map[string]any{"size": json.Number("12345678901234567890")}}}
	docs, err := Decode([]string{doc})
	if err != nil {
		t.Fatal(err)
	}
	facts := serve(t, f).For(docs, nil)
	for range 2 {
		v, err := facts.Fact("recorded")
		if err != nil || !reflect.DeepEqual(v, f.values["recorded"]) {
			t.Errorf("recorded = %#v, %v", v, err)
		}
	}
	if !slices.Equal(f.asked, []string{"recorded"}) {
		t.Errorf("asked %v, want recorded once", f.asked)
	}
	// The input is what chalkd's own loader gives a command of a delivery to the running image.
	got, _ := json.Marshal(f.inputs[0])
	want, _ := json.Marshal(map[string]any{"config": rules.HashedConfigOf(docs), "oldConfig": rules.HashedConfigOf(nil)})
	if string(got) != string(want) {
		t.Errorf("input = %s\nwant %s", got, want)
	}
	f.err = errors.New("share.json is not JSON")
	if _, err := serve(t, f).For(docs, nil).Fact("share"); err == nil || err.Error() != "share.json is not JSON" {
		t.Errorf("err = %v, want the fact's error alone", err)
	}
}

// Local runs a checker in this process with the input and result as protobuf JSON, the image's
// system where it lies below the store's directory, and a facts socket the checker asks.
func TestLocal(t *testing.T) {
	root := t.TempDir()
	var system string
	l := Local{Check: func(ctx context.Context, sys, socket, _ string, stdin io.Reader, stdout io.Writer) error {
		system = sys
		data, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		in := &checkv1.Input{}
		if err := protojson.Unmarshal(data, in); err != nil {
			return err
		}
		v, err := DialFacts(ctx, socket).For(nil, nil).Fact("disks")
		if err != nil {
			return err
		}
		out, err := protojson.Marshal(&checkv1.Result{Documents: in.GetDelivered(), Identity: v.(string)})
		if err != nil {
			return err
		}
		_, err = io.Copy(stdout, bytes.NewReader(out))
		return err
	}}
	img := Image{Store: filepath.Join(root, "nix", "store"), System: "/nix/store/x"}
	res, err := l.Run(context.Background(), img, &checkv1.Input{Delivered: []string{doc}}, &fakeFacts{values: map[string]any{"disks": "vda"}})
	if err != nil {
		t.Fatal(err)
	}
	if system != filepath.Join(root, "nix/store/x") || !slices.Equal(res.GetDocuments(), []string{doc}) || res.GetIdentity() != "vda" {
		t.Errorf("system %s, result %v", system, res)
	}
}

// Redact's form of a config is what the checker gets: a Secret's value never reaches it.
func TestEncodeOfRedactedDocuments(t *testing.T) {
	docs, err := config.Parse(strings.NewReader("apiVersion: chalkos.dev/v1alpha1\nkind: Secret\nmetadata: {name: s}\nspec: {data: {k: s3cret}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Encode(config.Redact(docs))
	if err != nil || strings.Contains(out[0], "s3cret") {
		t.Errorf("Encode = %v, %v", out, err)
	}
}
