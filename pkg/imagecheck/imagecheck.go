// Package imagecheck runs the checker of an image that does not run, its `chalkd check`, from the
// image's own store, and serves it the node's facts. chalkd mounts the image's store read-only
// through dm-verity, as an upgrade checks one; the checker runs in a transient unit of systemd
// whose /nix/store is that store, so the checker, its renderers and every program they name are
// the image's own. It reads its input on standard input and writes its result to standard
// output, as protobuf JSON of chalkos.check.v1, and asks for facts one at a time over a Unix
// socket. The input carries the documents with their secret values hashed: the checker never
// sees a Secret's value.
package imagecheck

import (
	"context"
	"encoding/json"
	"fmt"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
	"github.com/trevex/chalkos/pkg/config"
)

// Image is an image whose checker runs: Store is where its store's /nix/store is on this node,
// System its NixOS system, a path of its store, and Checker where its chalkd is on this node:
// System's sw/bin/chalkd, its links followed within the image.
type Image struct {
	Store   string
	System  string
	Checker string
}

// CheckerPath is where an image's system holds the checker, its chalkd.
const CheckerPath = "/sw/bin/chalkd"

// Facts loads the node's facts for the checker, as the image declares them. input is what a
// command fact reads on standard input, config and oldConfig as rules see them.
type Facts interface {
	Fact(name string, input map[string]any) (any, error)
}

// Runner runs an image's checker on in and returns its result. An error is a checker that could
// not run or did not answer; a refusal is part of the result.
type Runner interface {
	Run(ctx context.Context, img Image, in *checkv1.Input, f Facts) (*checkv1.Result, error)
}

// Encode is docs as the input and the result carry them: each document one JSON object.
func Encode(docs []config.Document) ([]string, error) {
	out := make([]string, len(docs))
	for i, d := range docs {
		data, err := json.Marshal(d.Object)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", d, err)
		}
		out[i] = string(data)
	}
	return out, nil
}

// Decode is the documents the input or the result carries, their numbers kept exact.
func Decode(objects []string) ([]config.Document, error) {
	docs := make([]config.Document, len(objects))
	for i, o := range objects {
		d, err := config.FromJSON([]byte(o))
		if err != nil {
			return nil, fmt.Errorf("document %d: %w", i+1, err)
		}
		docs[i] = d
	}
	return docs, nil
}
