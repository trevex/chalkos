package storage

import "fmt"

// DiskRef is a disk as the SystemDisk and Volume kinds name it: by its path or by selector keys,
// each null where it is not given.
type DiskRef struct {
	Path   *string `json:"path"`
	Model  *string `json:"model"`
	Serial *string `json:"serial"`
	WWN    *string `json:"wwn"`
	Size   *string `json:"size"`
	Type   *string `json:"type"`
}

// Ref is the reference d gives: its path, or else its selector.
func (d DiskRef) Ref() (Ref, error) {
	sel := Selector{}
	set := func(dst *string, v *string) bool {
		if v == nil {
			return false
		}
		*dst = *v
		return true
	}
	hasSelector := set(&sel.Model, d.Model)
	hasSelector = set(&sel.Serial, d.Serial) || hasSelector
	hasSelector = set(&sel.WWN, d.WWN) || hasSelector
	hasSelector = set(&sel.Size, d.Size) || hasSelector
	hasSelector = set(&sel.Type, d.Type) || hasSelector
	switch {
	case d.Path != nil && !hasSelector:
		return Ref{Path: *d.Path}, nil
	case d.Path == nil && hasSelector:
		return Ref{Selector: sel}, nil
	default:
		return Ref{}, fmt.Errorf("a disk is named by its path or selector keys (model, serial, wwn, size, type), not both or neither")
	}
}
