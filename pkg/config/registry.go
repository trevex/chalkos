package config

import "fmt"

// RegistryCredentials are what a Registry document gives a node to pull from its host with:
// Username and Password are empty for a registry pulled from anonymously.
type RegistryCredentials struct {
	Host, Username, Password string
}

// RegistryCredentialsOf returns the credentials of each Registry document among docs, in their
// order, with the password its secretRef names taken from the Secrets among docs. It reads
// documents as delivered, without the image's defaults, as chalkd reads an upgrade's config
// before it checks it against the image. Two documents of one host are refused, as a rule of the
// kind refuses them.
func RegistryCredentialsOf(docs []Document) ([]RegistryCredentials, error) {
	secrets := map[string]map[string]string{}
	for _, d := range docsOf(docs, CoreAPI, "Secret") {
		var spec secretSpec
		if err := d.decodeSpec(&spec); err != nil {
			return nil, err
		}
		secrets[d.Name] = spec.Data
	}
	var out []RegistryCredentials
	by := map[string]Document{}
	for _, d := range docsOf(docs, CoreAPI, "Registry") {
		var spec registrySpec
		if err := d.decodeSpec(&spec); err != nil {
			return nil, err
		}
		if other, ok := by[spec.Host]; ok {
			return nil, fmt.Errorf("%s and %s both name the host %s; give one Registry document per host", other, d, spec.Host)
		}
		by[spec.Host] = d
		c := RegistryCredentials{Host: spec.Host}
		if spec.Username != nil {
			c.Username = *spec.Username
		}
		if spec.Password != nil {
			ref := spec.Password.SecretRef
			v, ok := secrets[ref.Name][ref.Key]
			if !ok {
				return nil, fmt.Errorf("%s names key %q of Secret %q, which the config does not hold", d, ref.Key, ref.Name)
			}
			c.Password = v
		}
		out = append(out, c)
	}
	return out, nil
}
