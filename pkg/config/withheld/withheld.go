// Package withheld names the config kinds the node withholds from its services: a Secret, whose
// values reach a unit only as the credentials its kinds reference, and an Addon, which chalkd
// alone reads from the stored config. No renderer reads them, no effect fires on them and
// chalkcfg prints neither. The package imports nothing, so chalkcfg uses it without linking the
// config package.
package withheld

// The withheld kinds, by name.
const (
	Secret = "Secret"
	Addon  = "Addon"
)

// Kind tells whether the kind of that name is withheld from the node's services.
func Kind(kind string) bool { return kind == Secret || kind == Addon }
