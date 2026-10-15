package schema

import (
	"encoding/json"
	"testing"
)

const builtin = "../../test/fixtures/kinds/builtin"

func TestDefaultFillsMissingFields(t *testing.T) {
	s, err := LoadSchemas(builtin)
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, `{
		"apiVersion": "storage.chalkos.dev/v1alpha1", "kind": "SystemDisk", "metadata": {"name": "system"},
		"spec": {"disk": {"path": "/dev/vda"}, "var": {"size": "20G"}}
	}`)
	got, err := s.Default(doc)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got.(map[string]any)["spec"])
	want := `{"disk":{"model":null,"path":"/dev/vda","serial":null,"size":null,"type":null,"wwn":null},` +
		`"encryption":{"fallback":"recovery-key","mode":"tpm2"},"var":{"encryption":{"mode":null},"size":"20G"}}`
	if string(b) != want {
		t.Errorf("spec = %s\nwant   %s", b, want)
	}
}

func TestDefaultKeepsGivenValuesAndFillsListItems(t *testing.T) {
	s, err := LoadSchemas(builtin)
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, `{
		"apiVersion": "time.chalkos.dev/v1alpha1", "kind": "TimeSync", "metadata": {"name": "time"},
		"spec": {"servers": [{"host": "ntp.lan", "nts": false}]}
	}`)
	got, err := s.Default(doc)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got.(map[string]any)["spec"])
	if want := `{"dhcp":false,"servers":[{"host":"ntp.lan","nts":false,"port":null}]}`; string(b) != want {
		t.Errorf("spec = %s, want %s", b, want)
	}
}

func TestDefaultFillsAWholeMissingSpecField(t *testing.T) {
	s, err := LoadSchemas(builtin)
	if err != nil {
		t.Fatal(err)
	}
	doc := decode(t, `{"apiVersion": "time.chalkos.dev/v1alpha1", "kind": "TimeSync", "metadata": {"name": "time"}, "spec": {}}`)
	got, err := s.Default(doc)
	if err != nil {
		t.Fatal(err)
	}
	servers := got.(map[string]any)["spec"].(map[string]any)["servers"].([]any)
	if len(servers) != 3 {
		t.Fatalf("servers = %v", servers)
	}
}

func TestDefaultDoesNotShareDefaultValues(t *testing.T) {
	s, err := LoadSchemas(builtin)
	if err != nil {
		t.Fatal(err)
	}
	doc := func() any {
		return decode(t, `{"apiVersion": "time.chalkos.dev/v1alpha1", "kind": "TimeSync", "metadata": {"name": "t"}, "spec": {}}`)
	}
	a, _ := s.Default(doc())
	a.(map[string]any)["spec"].(map[string]any)["servers"].([]any)[0].(map[string]any)["host"] = "changed"
	b, _ := s.Default(doc())
	if host := b.(map[string]any)["spec"].(map[string]any)["servers"].([]any)[0].(map[string]any)["host"]; host != "ptbtime1.ptb.de" {
		t.Errorf("a changed default leaked into another document: %v", host)
	}
}

func TestRawReturnsTheSchemaDocument(t *testing.T) {
	s, err := LoadSchemas(builtin)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := s.Raw("chalkos.dev/v1alpha1", "Node")
	if !ok || raw.(map[string]any)["title"] != "Node" {
		t.Fatalf("Raw = %v, %v", raw, ok)
	}
	if _, ok := s.Raw("chalkos.dev/v1alpha1", "Nope"); ok {
		t.Error("Raw found an unknown kind")
	}
}
