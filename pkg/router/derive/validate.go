package derive

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/trevex/chalkos/pkg/refusals"
)

// Validate is the validator router: chalkd runs it as chalkos-render validate router on the
// documents of the kinds of Kinds that the image declares, and chalkctl links it and runs it
// in-process on the same documents, so both refuse a config alike. stdin is what a validator
// reads on standard input, the input a renderer reads too: the node's name and the documents of
// each kind, with their schema defaults and their secret values hashed. Validate builds the model
// of the documents as Build does and returns every refusal of the checks that check runs, each
// once; chalkos-render and chalkctl replace the runes of their messages that refusals.Unprintable
// reports by spaces. An input that is no such object, and a guard's internal error, are its error,
// the validator's failure: chalkd and chalkctl run the validator after the schemas, the
// references and the rules have checked the documents, which keeps every guard from firing.
func Validate(stdin []byte) ([]refusals.Refusal, error) {
	byKind, err := readDocuments(stdin)
	if err != nil {
		return nil, err
	}
	rs, others := refusals.Split(check(byKind))
	if len(others) > 0 {
		return nil, errors.Join(others...)
	}
	return rs, nil
}

// check runs the router's checks of what the documents of byKind mean together, which no schema
// or rule sees, and joins their refusals: the VRFs' links as Build checks them, the NAT64s as
// NAT64Instances checks them, the firewall's zones, NAT rules, management interfaces and policy
// routes' in, the BGP peers' sessions, the resolvers' addresses, the DHCP subnets and relays, and
// the WireGuards and their peers.
// Each goes on past a refusal to the next document, so a config with several faults gets a
// refusal for each. The checks that read the links' VRFs run once Build accepts them, since a
// refusal of Build leaves them wrong, and those that read the NAT64s' pools once NAT64Instances
// accepts them. A check refuses what the outputs that apply its documents would apply otherwise
// than the documents say, so it runs whether or not the image renders those outputs: an image
// without a feature declares none of its kinds, whose documents the validator then never reads.
func check(byKind map[string]any) error {
	d, err := Build(byKind)
	nat64, nerr := d.NAT64Instances()
	if err != nil {
		return errors.Join(err, nerr)
	}
	_, serr := d.Sessions()
	_, derr := d.Subnets()
	_, rerr := d.Relays()
	_, werr := d.WireGuardInterfaces()
	return errors.Join(nerr, d.checkFirewall(nat64), serr, d.checkResolvers(), derr, rerr, werr)
}

// readDocuments is the documents of data, a validator's input, by kind, as Build takes them: each
// kind's documents a list sorted by name, with the numbers exact. A kind whose value is no list is
// an error.
func readDocuments(data []byte) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var in struct {
		Documents map[string][]any `json:"documents"`
	}
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("the input is no object of documents by kind: %w", err)
	}
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return nil, errors.New("the input is no object of documents by kind: more follows the object")
	}
	if in.Documents == nil {
		return nil, errors.New("the input holds no documents")
	}
	byKind := make(map[string]any, len(in.Documents))
	for kind, docs := range in.Documents {
		byKind[kind] = docs
	}
	return byKind, nil
}
