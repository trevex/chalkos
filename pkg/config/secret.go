package config

// SecretValues are the values of a config's Secrets, by Secret name and key.
type SecretValues map[string]map[string]string

// Secrets returns the values of the Secrets among the documents. A value that is not a string is left
// out: the schema of a Secret allows strings alone.
func (docs Documents) Secrets() SecretValues {
	out := SecretValues{}
	for _, d := range docs {
		if !d.IsSecret() {
			continue
		}
		spec, _ := d.Object["spec"].(map[string]any)
		data, _ := spec["data"].(map[string]any)
		values := make(map[string]string, len(data))
		for k, v := range data {
			if s, ok := v.(string); ok {
				values[k] = s
			}
		}
		out[d.Name] = values
	}
	return out
}

// Lookup returns the value a secretRef names, and whether the Secrets hold it.
func (v SecretValues) Lookup(ref SecretRef) (string, bool) {
	s, ok := v[ref.Name][ref.Key]
	return s, ok
}
