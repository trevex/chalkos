package router

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"regexp"
	"slices"
	"strconv"
)

// NAT64Protocols are the protocols Jool translates, in the order the status lists them.
var NAT64Protocols = []string{"tcp", "udp", "icmp"}

// joolName is the name of an instance as chalkos's NAT64 documents give them, which reaches
// jool's command line.
var joolName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]{0,13}[a-z0-9])?$`)

// JoolInstancesArgs are jool's arguments that list its instances.
var JoolInstancesArgs = []string{"instance", "display", "--csv", "--no-headers"}

// JoolTableArgs are jool's arguments that list one protocol's entries of an instance's table:
// session, bib or pool4. Addresses print as numbers, as Jool would ask DNS for names otherwise.
func JoolTableArgs(instance, table, protocol string) []string {
	args := []string{"-i", instance, table, "display", "--csv", "--no-headers", "--" + protocol}
	if table != "pool4" {
		args = append(args, "--numeric")
	}
	return args
}

// joolRows are the records of jool's CSV, whose lines may differ in their number of fields.
func joolRows(data []byte) ([][]string, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read jool's CSV: %w", err)
	}
	return rows, nil
}

// ParseJoolInstances reads the names of jool instance display --csv --no-headers, a line of
// namespace, name and framework per instance, sorted. A name chalkos would not give an instance is
// an error, since the names reach jool's command line.
func ParseJoolInstances(data []byte) ([]string, error) {
	rows, err := joolRows(data)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, row := range rows {
		if len(row) != 3 {
			return nil, fmt.Errorf("jool lists an instance as %q, not namespace, name and framework", row)
		}
		if !joolName.MatchString(row[1]) {
			return nil, fmt.Errorf("jool lists an instance named %q, which no NAT64 document names", row[1])
		}
		names = append(names, row[1])
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// CountJoolRows counts the entries of a session or BIB table jool displays with --csv
// --no-headers, each a line that starts with its protocol and holds at least BIB's six fields.
func CountJoolRows(data []byte) (uint64, error) {
	rows, err := joolRows(data)
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		if len(row) < 6 || !slices.Contains([]string{"TCP", "UDP", "ICMP"}, row[0]) {
			return 0, fmt.Errorf("jool lists %q, which is no entry of TCP, UDP or ICMP", row)
		}
	}
	return uint64(len(rows)), nil
}

// ParseJoolPool4 reads the size of a pool from jool pool4 display --csv --no-headers: the ports of
// each address, a line of mark, protocol, address, first and last port and the iterations each.
func ParseJoolPool4(data []byte) (uint64, error) {
	rows, err := joolRows(data)
	if err != nil {
		return 0, err
	}
	var size uint64
	for _, row := range rows {
		if len(row) < 5 {
			return 0, fmt.Errorf("jool lists a pool entry as %q, without its ports", row)
		}
		first, errFirst := strconv.ParseUint(row[3], 10, 16)
		last, errLast := strconv.ParseUint(row[4], 10, 16)
		if errFirst != nil || errLast != nil || first > last {
			return 0, fmt.Errorf("jool lists a pool entry with the ports %s to %s", row[3], row[4])
		}
		size += last - first + 1
	}
	return size, nil
}
