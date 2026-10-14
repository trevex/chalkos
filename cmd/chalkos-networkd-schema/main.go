// Command chalkos-networkd-schema writes the spec schemas of the networkd kinds from systemd's
// gperf tables: network.json, netdev.json and link.json in the output directory.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/networkdschema"
)

const usage = "usage: chalkos-networkd-schema NETWORK_GPERF NETDEV_GPERF LINK_GPERF OUTDIR"

func main() {
	log.SetFlags(0)
	log.SetPrefix("chalkos-networkd-schema: ")
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	out := os.Args[4]
	if err := os.MkdirAll(out, 0o755); err != nil {
		log.Fatal(err)
	}
	for i, name := range []string{"network", "netdev", "link"} {
		if err := write(os.Args[1+i], filepath.Join(out, name+".json")); err != nil {
			log.Fatal(err)
		}
	}
}

func write(table, path string) error {
	f, err := os.Open(table)
	if err != nil {
		return err
	}
	defer f.Close()
	entries, err := networkdschema.Parse(f)
	if err != nil {
		return fmt.Errorf("%s: %w", table, err)
	}
	s, err := networkdschema.SpecSchema(entries)
	if err != nil {
		return fmt.Errorf("%s: %w", table, err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}
