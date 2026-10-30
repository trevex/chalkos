package router

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"slices"
	"strconv"
)

// NAT64Protocols are the protocols Jool translates, in the order the status lists them.
var NAT64Protocols = []string{"tcp", "udp", "icmp"}

// JoolInstancesArgs are jool's arguments that list its instances.
var JoolInstancesArgs = []string{"instance", "display", "--csv", "--no-headers"}

// JoolMissingInstance is what jool says, after the kernel module's error, of an instance that does
// not exist in its network namespace, such as one removed while chalkd asks about it.
const JoolMissingInstance = "This namespace lacks an instance named"

// NAT64Namespace is the path of the network namespace in which chalkos-nat64 runs the Jool
// instance of the NAT64 document of the name, which ShortName matched.
func NAT64Namespace(instance string) string { return "/run/netns/nat64-" + instance }

// NamespaceMissing is what nsenter says of a namespace that does not exist, such as that of an
// instance removed while chalkd asks about it.
const NamespaceMissing = "nsenter: cannot open /run/netns/"

// InNamespace are nsenter's arguments that run jool with args in the instance's namespace.
func InNamespace(instance string, args []string) []string {
	return append([]string{"--net=" + NAT64Namespace(instance), "jool"}, args...)
}

// JoolStatsArgs are jool's arguments that list all of an instance's statistics, also those that
// are zero.
func JoolStatsArgs(instance string) []string {
	return []string{"-i", instance, "stats", "display", "--csv", "--no-headers", "--all"}
}

// JoolPool4Args are jool's arguments that list an instance's pool of one protocol.
func JoolPool4Args(instance, protocol string) []string {
	return []string{"-i", instance, "pool4", "display", "--csv", "--no-headers", "--" + protocol}
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
// namespace, name and framework per instance, sorted. Jool lists the instances of every network
// namespace; a name chalkos would not give an instance is left out, since the names reach jool's
// command line.
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
		if ShortName.MatchString(row[1]) {
			names = append(names, row[1])
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// ParseJoolStats reads an instance's counters of session and BIB entries from jool stats display
// --csv --no-headers --all, a line of name and value per statistic, and an explanation with
// --explain.
func ParseJoolStats(data []byte) (sessions, bindings uint64, err error) {
	rows, err := joolRows(data)
	if err != nil {
		return 0, 0, err
	}
	stats := map[string]uint64{}
	for _, row := range rows {
		if len(row) < 2 || len(row) > 3 {
			return 0, 0, fmt.Errorf("jool lists %q, not a statistic and its value", row)
		}
		if row[0] != "JSTAT_SESSIONS" && row[0] != "JSTAT_BIB_ENTRIES" {
			continue
		}
		v, err := strconv.ParseUint(row[1], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("jool lists %s of %q", row[0], row[1])
		}
		stats[row[0]] = v
	}
	for _, name := range []string{"JSTAT_SESSIONS", "JSTAT_BIB_ENTRIES"} {
		if _, ok := stats[name]; !ok {
			return 0, 0, fmt.Errorf("jool's statistics lack %s", name)
		}
	}
	return stats["JSTAT_SESSIONS"], stats["JSTAT_BIB_ENTRIES"], nil
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
