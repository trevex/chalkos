// Command veritysize prints the bytes of an image's store data and of its hash tree as package
// verity reads them from the verity superblock. The image-size check compares its output with
// that of chalkos-image-size measure, which every image build runs.
//
//	veritysize RAW REPART_OUTPUT
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/trevex/chalkos/pkg/verity"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "veritysize:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: veritysize RAW REPART_OUTPUT")
	}
	report, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}
	var partitions []struct {
		Type   string `json:"type"`
		Offset int64  `json:"offset"`
	}
	if err := json.Unmarshal(report, &partitions); err != nil {
		return fmt.Errorf("%s: %w", args[1], err)
	}
	var offsets []int64
	for _, p := range partitions {
		if strings.HasSuffix(p.Type, "-verity") {
			offsets = append(offsets, p.Offset)
		}
	}
	if len(offsets) != 1 {
		return fmt.Errorf("%s: %d verity partitions, want one", args[1], len(offsets))
	}
	raw, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer raw.Close()
	b := make([]byte, verity.SuperblockSize)
	if _, err := raw.ReadAt(b, offsets[0]); err != nil {
		return err
	}
	sb, err := verity.ParseSuperblock(b)
	if err != nil {
		return fmt.Errorf("%s at offset %d: %w", args[0], offsets[0], err)
	}
	fmt.Println(sb.DataSize(), sb.HashSize())
	return nil
}
