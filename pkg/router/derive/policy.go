package derive

import (
	"cmp"
	"net/netip"
	"slices"
)

// NextHop is a NextHop document, its address parsed.
type NextHop struct {
	Name    string
	Address netip.Addr
	Link    string
	Table   uint32
}

// PolicyTables are the tables a PolicyRoute rule's target names: VRFs, the VRF documents' by
// name, and the NextHop documents', NextHops by name and Hops sorted by table.
type PolicyTables struct {
	VRFs     map[string]uint32
	NextHops map[string]NextHop
	Hops     []NextHop
}

// PolicyTables reads the tables of the VRF and NextHop documents. Its guard refuses table 0, which
// networkd refuses and the schema's minimum refuses before chalkd renders.
func (d Model) PolicyTables() (PolicyTables, error) {
	t := PolicyTables{VRFs: map[string]uint32{}, NextHops: map[string]NextHop{}}
	for _, v := range d.VRFs {
		t.VRFs[v.Name] = v.Spec.Table
	}
	for _, h := range d.NextHops {
		if h.Spec.Table == 0 {
			return t, InternalErrorf("NextHop %s: table 0 is no table", h.Name)
		}
		hop := NextHop{Name: h.Name, Address: h.Spec.Address, Link: h.Spec.Interface, Table: h.Spec.Table}
		t.NextHops[h.Name] = hop
		t.Hops = append(t.Hops, hop)
	}
	slices.SortStableFunc(t.Hops, func(a, b NextHop) int { return cmp.Compare(a.Table, b.Table) })
	return t, nil
}

// Table is the table of a rule's target: its VRF's table, its own table or its NextHop's table. Its
// guards refuse a to of no target, which the kind's rules refuse, a VRF or NextHop the input
// lacks, which the reference check refuses, and table 0, which the schema refuses.
func (t PolicyTables) Table(r PolicyRule) (uint32, error) {
	var table uint32
	switch to := r.To; {
	case to.VRF != nil:
		v, err := MustGet(t.VRFs, "VRF", *to.VRF)
		if err != nil {
			return 0, err
		}
		table = v
	case to.Table != nil:
		table = *to.Table
	case to.NextHop != nil:
		h, err := MustGet(t.NextHops, "NextHop", *to.NextHop)
		if err != nil {
			return 0, err
		}
		table = h.Table
	default:
		return 0, InternalErrorf("to names no target")
	}
	if table == 0 {
		return 0, InternalErrorf("table 0 is no table")
	}
	return table, nil
}
