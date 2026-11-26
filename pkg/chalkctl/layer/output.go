package layer

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/trevex/chalkos/pkg/internal/cli"
)

// Output is the format a read command prints in, as -o, --output gives it.
type Output string

// The formats of Output.
const (
	// TextOutput is for people, and changes with the wording of chalkctl.
	TextOutput Output = "text"
	// JSONOutput is for programs: the protojson of the node API's messages the command reads,
	// whose field names are the API's, or the fields the command's help documents.
	JSONOutput Output = "json"
)

// Register registers -o, --output, which defaults to text.
func (o *Output) Register(fs *pflag.FlagSet) {
	*o = TextOutput
	fs.VarP(cli.Enum(o, "format", TextOutput, JSONOutput), "output", "o", "format to print: text, or json for programs")
}

// JSON reports whether the command prints JSON.
func (o Output) JSON() bool { return o == JSONOutput }

// ProtoJSON is m as protojson, with the API's field names in lowerCamelCase and fields at their
// zero value left out, for a field of what WriteJSON writes.
func ProtoJSON(m proto.Message) (json.RawMessage, error) {
	data, err := protojson.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", m.ProtoReflect().Descriptor().FullName(), err)
	}
	return data, nil
}

// WriteJSON writes v to w as indented JSON and a line break; a proto message is written as
// ProtoJSON gives it. The output is the same for the same v, as protojson's alone is not.
func WriteJSON(w io.Writer, v any) error {
	if m, ok := v.(proto.Message); ok {
		data, err := ProtoJSON(m)
		if err != nil {
			return err
		}
		v = data
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
