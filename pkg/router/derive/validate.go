package derive

import (
	"errors"
	"fmt"

	"github.com/trevex/chalkos/pkg/jsonfile"
	"github.com/trevex/chalkos/pkg/refusals"
)

// Validate is the validator router. chalkd runs it as chalkos-render validate router and chalkctl
// runs it in-process, so both refuse a config alike. stdin holds the node's name and the
// documents by kind, with schema defaults and hashed secrets. It returns each refusal once; bad
// input or a guard's internal error is its error.
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

// check runs every check that spans documents and joins their refusals, one per fault. The checks
// that read the links' VRFs run only once Build accepts them, those that read the NAT64s' pools
// once NAT64Instances does. A check runs whether or not the image renders its output; an image
// without a feature declares none of its kinds.
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
	var in struct {
		Documents map[string][]any `json:"documents"`
	}
	if err := jsonfile.DecodeExact(data, &in); err != nil {
		return nil, fmt.Errorf("the input is no object of documents by kind: %w", err)
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
