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

// ErrTooManyPDRecords is the error of ReadPDRecords past MaxPDRecords live records.
var ErrTooManyPDRecords = fmt.Errorf("more than %d records of delegated prefixes", MaxPDRecords)

// ReadPDRecords returns the routes of the live records in dir that a pool delegates on the
// record's link. The user kea writes dir, so it opens no links, reads regular files alone without
// blocking, and refuses a dir others may write. Past MaxPDRecords live records it stops and
// returns ErrTooManyPDRecords with the routes so far; ended records count toward no limit. A
// missing dir holds no records.
func ReadPDRecords(dir string, pools []PDPool, now time.Time) ([]PDRoute, error) {
	return readPDRecords(dir, pools, now, MaxPDRecords)
}

// readPDRecords is ReadPDRecords with the limit as a parameter, for tests.
func readPDRecords(dir string, pools []PDPool, now time.Time, limit int) ([]PDRoute, error) {
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
	live := 0
	for {
		names, err := d.Readdirnames(256)
		for _, name := range names {
			if strings.HasPrefix(name, ".") { // the hook's half-written files
				continue
			}
			link, prefix, ok := pdRecordName(name)
			if !ok {
				continue
			}
			data, ok := readPDRecord(fd, name)
			if !ok {
				continue
			}
			via, ends, ok := pdRecord(data)
			if !ok || !ends.After(now) {
				continue
			}
			if live == limit {
				return sortPDRoutes(routes), ErrTooManyPDRecords
			}
			live++
			if r, ok := pdRoute(link, prefix, via, ends, pools, now); ok {
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

// readPDRecord reads the record name in dir: a regular file of maxPDRecordSize bytes at most,
// opened without following a link or waiting for a writer.
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
	data, err := io.ReadAll(io.LimitReader(f, maxPDRecordSize+1))
	if err != nil || len(data) > maxPDRecordSize {
		return nil, false
	}
	return data, true
}
