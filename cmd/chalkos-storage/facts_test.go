package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunFacts(t *testing.T) {
	if err := runFacts(nil, strings.NewReader("{}"), &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "disks or fallback") {
		t.Errorf("no fact: err = %v", err)
	}
	// Without a fallback secret nothing is read from the node.
	var out bytes.Buffer
	if err := runFacts([]string{"fallback"}, strings.NewReader(`{"config": {}, "oldConfig": {}}`), &out); err != nil || out.String() != `{"sent":false,"matches":null}` {
		t.Errorf("fallback = %q, %v", out.String(), err)
	}
}
