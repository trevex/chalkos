package inventory

import "encoding/json"

// Encode writes an inventory of the name and spec. It is JSON, which YAML reads.
func Encode(name string, spec Spec) ([]byte, error) {
	data, err := json.MarshalIndent(Document{APIVersion: APIVersion, Kind: Kind, Metadata: Metadata{Name: name}, Spec: spec}, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
