package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/trevex/chalkos/pkg/jsonfile"
)

// checkKea checks the output kea: kea-dhcp4 -t or kea-dhcp6 -t parses each config file. Both
// refuse a config of links the check's network namespace lacks, so each gets its config without
// its links: no interfaces, and no subnet's interface.
func checkKea(dhcp4, dhcp6, out string) error {
	files, err := filepath.Glob(filepath.Join(out, "*.json"))
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "chalkos-check-kea-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for _, f := range files {
		name := filepath.Base(f)
		program, key := dhcp6, "Dhcp6"
		if strings.HasPrefix(name, "kea-dhcp4") {
			program, key = dhcp4, "Dhcp4"
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		unlinked, err := keaWithoutLinks(data, key)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, unlinked, 0o600); err != nil {
			return err
		}
		if err := runCheck([]string{program, "-t", path}); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

// keaWithoutLinks is the Kea config data, whose server is the one of key, Dhcp4 or Dhcp6,
// without the links it names: its interfaces-config lists none, and no subnet6 names one.
func keaWithoutLinks(data []byte, key string) ([]byte, error) {
	var conf map[string]any
	if err := jsonfile.DecodeExact(data, &conf); err != nil {
		return nil, err
	}
	server, ok := conf[key].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the config holds no %s server", key)
	}
	ifaces, _ := server["interfaces-config"].(map[string]any)
	if ifaces == nil {
		ifaces = map[string]any{}
		server["interfaces-config"] = ifaces
	}
	ifaces["interfaces"] = []any{}
	subnets, _ := server["subnet6"].([]any)
	for _, s := range subnets {
		if s, ok := s.(map[string]any); ok {
			delete(s, "interface")
		}
	}
	return json.Marshal(conf)
}
