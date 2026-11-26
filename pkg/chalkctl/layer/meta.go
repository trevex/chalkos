package layer

import (
	"github.com/spf13/cobra"

	"github.com/trevex/chalkos/pkg/pki"
)

// Credentials are what a command authenticates with.
type Credentials string

// The credentials a command declares. A client file of a role works for the commands that need
// that role or a lower one, and the secrets file for every command that takes credentials.
const (
	// NoCredentials is a command that takes neither the secrets file nor a client file.
	NoCredentials Credentials = "none"
	// SecretsFile is a command that takes the secrets file alone.
	SecretsFile Credentials = "secrets"
	Reader      Credentials = Credentials(pki.RoleReader)
	Operator    Credentials = Credentials(pki.RoleOperator)
	Admin       Credentials = Credentials(pki.RoleAdmin)
)

// Role is the least role of a client file that the credentials accept; "" for NoCredentials and
// SecretsFile, which take no client file.
func (c Credentials) Role() pki.Role {
	switch c {
	case Reader, Operator, Admin:
		return pki.Role(c)
	}
	return ""
}

// Meta is what a command declares of itself, once, in its annotations: chalkctl generates the
// sentences of its help that name its credentials and its confirmation from it, and registers
// --yes on a command that confirms.
type Meta struct {
	Credentials Credentials
	// Confirms says when a destructive command asks on the terminal, completing "The command",
	// such as "asks for the node's name before it wipes the volume". Host.Confirm asks; empty
	// for a command that destroys nothing.
	Confirms string
}

// The annotations of a command that hold its Meta.
const (
	credentialsAnnotation = "chalkos.dev/credentials"
	confirmsAnnotation    = "chalkos.dev/confirms"
)

// Annotations are the annotations of a command with the meta, for cobra.Command.Annotations.
func (m Meta) Annotations() map[string]string {
	a := map[string]string{credentialsAnnotation: string(m.Credentials)}
	if m.Confirms != "" {
		a[confirmsAnnotation] = m.Confirms
	}
	return a
}

// MetaOf is the meta cmd declares; its Credentials are empty when it declares none.
func MetaOf(cmd *cobra.Command) Meta {
	return Meta{Credentials: Credentials(cmd.Annotations[credentialsAnnotation]), Confirms: cmd.Annotations[confirmsAnnotation]}
}
