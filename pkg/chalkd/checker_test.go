package chalkd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/identity"
	"github.com/trevex/chalkos/pkg/imagecheck"
)

// noFacts are the facts of a node with none: every fact is null.
type noFacts struct{}

func (noFacts) Fact(string, map[string]any) (any, error) { return nil, nil }

// checkWith has the image of st check text for the node n1 on metal, as chalkd check does.
func checkWith(t *testing.T, s *Server, st dirStore, text string) *checkv1.Result {
	t.Helper()
	docs, err := config.Parse(strings.NewReader(text))
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := imagecheck.Encode(config.Redact(docs))
	if err != nil {
		t.Fatal(err)
	}
	res, err := imagecheck.Local{Check: s.Check}.Run(context.Background(), imagecheck.Image{Store: st.Dir(), System: "/nix/store/x"},
		&checkv1.Input{Node: "n1", Platform: "metal", Delivered: delivered}, noFacts{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// chalkd check translates the config with the schemas of the image's system: it gives the
// documents with the image's defaults and their secret values hashed, and the identity.
func TestCheckGivesTheDocumentsAndTheIdentity(t *testing.T) {
	s, _ := configServer(t)
	res := checkWith(t, s, imageWith(t, s, fixtureSchemas(t), ""), baseConfig)
	if res.GetRefusal() != nil || res.GetError() != "" {
		t.Fatalf("result = %v", res)
	}
	docs, err := imagecheck.Decode(res.GetDocuments())
	if err != nil {
		t.Fatal(err)
	}
	var demo, secret config.Document
	for _, d := range docs {
		switch d.Kind {
		case "Demo":
			demo = d
		case "Secret":
			secret = d
		}
	}
	if demo.Object["spec"].(map[string]any)["mode"] != "safe" {
		t.Errorf("Demo = %v, want the schema's default mode", demo.Object)
	}
	if data, _ := json.Marshal(secret.Object); strings.Contains(string(data), "s3cret") {
		t.Errorf("Secret = %s, want its value hashed", data)
	}
	var id identity.Identity
	if err := json.Unmarshal([]byte(res.GetIdentity()), &id); err != nil || id.Hostname != "n1" || id.Platform != "metal" {
		t.Errorf("identity = %s, %v", res.GetIdentity(), err)
	}
}

// chalkd check refuses config the image's schemas do not accept, as a delivery refuses it, and
// answers with an error for an image whose own files it cannot read.
func TestCheckRefuses(t *testing.T) {
	s, _ := configServer(t)
	withoutDemo := fixtureSchemas(t)
	delete(withoutDemo, "demo.example.org_v1alpha1_Demo.json")
	res := checkWith(t, s, imageWith(t, s, withoutDemo, ""), baseConfig)
	if r := res.GetRefusal(); r.GetStage() != "config" || !strings.HasPrefix(r.GetMessage(), "the config: ") || !strings.Contains(r.GetMessage(), `does not accept kind "Demo"`) {
		t.Errorf("refusal = %v", r)
	}
	broken := imageWith(t, s, nil, "")
	if err := os.RemoveAll(filepath.Join(string(broken), "nix/store/x/etc/chalkos/schemas")); err != nil {
		t.Fatal(err)
	}
	if res := checkWith(t, s, broken, baseConfig); !strings.HasPrefix(res.GetError(), "the image's schemas: ") {
		t.Errorf("result = %v, want the image's error", res)
	}
}
