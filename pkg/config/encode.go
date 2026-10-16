package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Encoded is documents as chalkctl sends them: Text holds one JSON document each, separated by
// ---, Version is the hash chalkd computes from that text, and Documents are the documents as
// chalkd reads them back.
type Encoded struct {
	Documents []Document
	Text      string
	Version   string
}

// Encode writes docs as the YAML stream chalkctl sends and reads it back as chalkd does, so the
// documents and their version are those of the text.
func Encode(docs []Document) (Encoded, error) {
	parts := make([]string, 0, len(docs))
	for _, d := range docs {
		b, err := json.Marshal(d.Object)
		if err != nil {
			return Encoded{}, fmt.Errorf("%s: %w", d, err)
		}
		parts = append(parts, string(b))
	}
	text := strings.Join(parts, "\n---\n") + "\n"
	read, err := Parse(strings.NewReader(text))
	if err != nil {
		return Encoded{}, err
	}
	version, err := Hash(read)
	if err != nil {
		return Encoded{}, err
	}
	return Encoded{Documents: read, Text: text, Version: version}, nil
}
