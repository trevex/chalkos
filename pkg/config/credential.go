package config

import (
	"fmt"
	"strings"
)

// CredentialPrefix is the ID a granted unit's LoadCredential= gives its credentials directory.
// systemd loads each file in it as the credential <CredentialPrefix>_<file>, so the Nix module's
// LoadCredential= must use the same prefix for CheckCredential to hold.
const CredentialPrefix = "secret"

// CredentialFile is the name of the file that carries the value ref names in a granted unit's
// credentials directory.
func CredentialFile(ref SecretRef) string { return ref.Name + "." + ref.Key }

// CheckCredential refuses a reference whose file would not become a credential: systemd fails a
// unit whose credentials directory holds a file whose credential ID is not valid. Secret and key
// do not start with a dot, as the slot package's own temporary files do.
func CheckCredential(ref SecretRef) error {
	if ref.Name == "" || ref.Key == "" || strings.HasPrefix(ref.Name, ".") || strings.HasPrefix(ref.Key, ".") ||
		!credentialNameValid(CredentialPrefix+"_"+CredentialFile(ref)) {
		return fmt.Errorf("key %q of Secret %q cannot name a credential: use printable ASCII without / or :, not starting with a dot, at most %d bytes for Secret and key together", ref.Key, ref.Name, maxCredentialName-len(CredentialPrefix)-2)
	}
	return nil
}

// maxCredentialName is systemd's FDNAME_MAX and NAME_MAX, which both bound a credential name.
const maxCredentialName = 255

// credentialNameValid is systemd's credential_name_valid: a valid file name (not empty, . or ..,
// no /, at most NAME_MAX bytes) that is also a valid fd name (bytes 32 to 126 alone, no :).
func credentialNameValid(s string) bool {
	if s == "" || s == "." || s == ".." || len(s) > maxCredentialName {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < ' ' || c > '~' || c == ':' || c == '/' {
			return false
		}
	}
	return true
}
