package router

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// ErrTooManyPDRecords is the error of ReadPDRecords when the directory holds more than
// MaxPDRecords records, of which it read MaxPDRecords.
var ErrTooManyPDRecords = fmt.Errorf("more than %d records of delegated prefixes", MaxPDRecords)

// ReadPDRecords reads the records of delegated prefixes in dir, which the user kea writes, and
// returns the routes of those whose leases have not ended at now and that a pool of pools
// delegates on the record's link, by its range and length, to a link-local address. It ignores
// the other records and files whose names start with a dot, the hook's own, half written. A
// missing dir holds no records. It opens neither a dir nor a record that is a symbolic link,
// refuses a dir that every user may write, reads regular files alone, without waiting, and
// MaxPDRecordSize bytes of each, and MaxPDRecords records at most: beyond those, it returns the
// routes it read and ErrTooManyPDRecords.
func ReadPDRecords(dir string, pools []PDPool, now time.Time) ([]PDRoute, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open the records of delegated prefixes %s, which must be a directory: %w", dir, err)
	}
	d := os.NewFile(uintptr(fd), dir)
	defer d.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("%s: %w", dir, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&0o002 != 0 {
		return nil, fmt.Errorf("%s is no directory that only its owner writes", dir)
	}
	var routes []PDRoute
	read := 0
	for {
		names, err := d.Readdirnames(256)
		for _, name := range names {
			if strings.HasPrefix(name, ".") {
				continue
			}
			if read == MaxPDRecords {
				return sortPDRoutes(routes), ErrTooManyPDRecords
			}
			read++
			link, prefix, ok := pdRecordName(name)
			if !ok {
				continue
			}
			data, ok := readPDRecord(fd, name)
			if !ok {
				continue
			}
			if r, ok := pdRoute(link, prefix, data, pools, now); ok {
				routes = append(routes, r)
			}
		}
		if errors.Is(err, io.EOF) {
			return sortPDRoutes(routes), nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
	}
}

// sortPDRoutes sorts routes by their prefixes, as the directory lists records in no order.
func sortPDRoutes(routes []PDRoute) []PDRoute {
	slices.SortFunc(routes, func(a, b PDRoute) int {
		return cmp.Or(a.Prefix.Addr().Compare(b.Prefix.Addr()), cmp.Compare(a.Prefix.Bits(), b.Prefix.Bits()), strings.Compare(a.Link, b.Link))
	})
	return routes
}

// readPDRecord reads the record name in the directory dir: a regular file of MaxPDRecordSize
// bytes at most, opened without following a link and without waiting for a writer.
func readPDRecord(dir int, name string) ([]byte, bool) {
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, false
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxPDRecordSize+1))
	if err != nil || len(data) > MaxPDRecordSize {
		return nil, false
	}
	return data, true
}
