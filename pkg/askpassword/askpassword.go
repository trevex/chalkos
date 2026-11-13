// Package askpassword answers systemd's password requests as a password agent does, by systemd's
// protocol (docs/PASSWORD_AGENTS.md): each request is an ask.* file of the ini format in
// /run/systemd/ask-password, which names a datagram socket; the agent sends "+" and the password
// to it, or "-" to cancel. The asking process accepts answers of root and of its own user alone.
package askpassword

import (
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Dir is where systemd's password requests wait.
const Dir = "/run/systemd/ask-password"

// MaxPassword is the longest password, in bytes, that the asking process takes: it reads a
// datagram of LINE_MAX bytes and the "+" before the password, and drops a longer one.
const MaxPassword = 2048

// Request is one password request: the [Ask] section of its file.
type Request struct {
	// Path is the request's file.
	Path string
	// PID is the asking process; zero when the file names none.
	PID int
	// Socket is the datagram socket the answer goes to.
	Socket       string
	AcceptCached bool
	// Echo says the password may show as it is typed; Silent says not even its length may.
	Echo   bool
	Silent bool
	// NotAfter is the CLOCK_MONOTONIC time in microseconds the request ends at; zero, or the
	// largest value as systemd's infinity, for none.
	NotAfter uint64
	// Message is the question, as systemd wrote it; it may hold any byte but a newline.
	Message string
	Icon    string
	// ID names what asks, such as cryptsetup:/dev/vdb1.
	ID string
	// Since is the file's modification time, which orders the requests.
	Since time.Time
}

// Ended reports whether the request is past NotAfter at now, a CLOCK_MONOTONIC time.
func (r Request) Ended(now time.Duration) bool {
	if r.NotAfter == 0 || r.NotAfter == math.MaxUint64 || now < 0 {
		return false
	}
	return uint64(now/time.Microsecond) > r.NotAfter
}

// Parse reads a request's file. As systemd's agents do, it leaves a field whose value does not
// parse at its default; a request without an absolute socket path it refuses.
func Parse(path string, data []byte) (Request, error) {
	r := Request{Path: path}
	section := ""
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line[0] == '#' || line[0] == ';':
			continue
		case line[0] == '[' && line[len(line)-1] == ']':
			section = line[1 : len(line)-1]
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section != "Ask" {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "PID":
			if pid, err := strconv.Atoi(value); err == nil && pid > 0 {
				r.PID = pid
			}
		case "Socket":
			r.Socket = value
		case "AcceptCached":
			r.AcceptCached = parseBool(value, r.AcceptCached)
		case "Echo":
			r.Echo = parseBool(value, r.Echo)
		case "Silent":
			r.Silent = parseBool(value, r.Silent)
		case "NotAfter":
			if us, err := strconv.ParseUint(value, 10, 64); err == nil {
				r.NotAfter = us
			}
		case "Message":
			r.Message = value
		case "Icon":
			r.Icon = value
		case "Id":
			r.ID = value
		}
	}
	if !filepath.IsAbs(r.Socket) {
		return Request{}, fmt.Errorf("%s names no socket", path)
	}
	return r, nil
}

// parseBool reads a boolean as systemd writes and reads it; def when s is none.
func parseBool(s string, def bool) bool {
	switch strings.ToLower(s) {
	case "1", "yes", "y", "true", "t", "on":
		return true
	case "0", "no", "n", "false", "f", "off":
		return false
	}
	return def
}

// Agent reads the requests of a directory. Zero fields mean the system's own: Dir, the
// CLOCK_MONOTONIC clock, and whether a process runs.
type Agent struct {
	Dir   string
	Now   func() time.Duration
	Alive func(pid int) bool
}

func (a Agent) dir() string { return cmp.Or(a.Dir, Dir) }

// Pending lists the requests that wait for an answer, oldest first: those not past NotAfter whose
// process runs. A file that went since the listing, or that does not parse, is left out.
func (a Agent) Pending() ([]Request, error) {
	entries, err := os.ReadDir(a.dir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now, alive := a.Now, a.Alive
	if now == nil {
		now = monotonic
	}
	if alive == nil {
		alive = running
	}
	var reqs []Request
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "ask.") || !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(a.dir(), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		r, err := Parse(path, data)
		if err != nil || r.Ended(now()) || (r.PID > 0 && !alive(r.PID)) {
			continue
		}
		r.Since = info.ModTime()
		reqs = append(reqs, r)
	}
	slices.SortFunc(reqs, func(x, y Request) int {
		return cmp.Or(x.Since.Compare(y.Since), strings.Compare(x.Path, y.Path))
	})
	return reqs, nil
}

// Watch signals on the channel when a request comes or goes in the directory, a signal for one
// or several changes, until ctx ends or the directory goes; the channel closes then, and a new
// Watch follows a directory made anew.
func (a Agent) Watch(ctx context.Context) (<-chan struct{}, error) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		return nil, fmt.Errorf("watch %s: %w", a.dir(), err)
	}
	mask := uint32(unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_DELETE | unix.IN_MOVED_FROM | unix.IN_ONLYDIR)
	if _, err := unix.InotifyAddWatch(fd, a.dir(), mask); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("watch %s: %w", a.dir(), err)
	}
	// A non-blocking descriptor reads through Go's poller, so closing the file ends a read.
	f := os.NewFile(uintptr(fd), "inotify")
	changes := make(chan struct{}, 1)
	stop := context.AfterFunc(ctx, func() { f.Close() })
	go func() {
		defer close(changes)
		defer stop()
		defer f.Close()
		buf := make([]byte, 64*(unix.SizeofInotifyEvent+unix.NAME_MAX+1))
		for {
			n, err := f.Read(buf)
			if err != nil {
				return
			}
			select {
			case changes <- struct{}{}:
			default:
			}
			if ignored(buf[:n]) {
				return
			}
		}
	}()
	return changes, nil
}

// ignored reports whether events, as inotify reads them, end the watch: the kernel removed it,
// as the directory went.
func ignored(events []byte) bool {
	for len(events) >= unix.SizeofInotifyEvent {
		mask := binary.NativeEndian.Uint32(events[4:8])
		size := unix.SizeofInotifyEvent + int(binary.NativeEndian.Uint32(events[12:16]))
		if mask&unix.IN_IGNORED != 0 {
			return true
		}
		events = events[min(size, len(events)):]
	}
	return false
}

// Answer sends the password to the request's socket. The datagram it builds is cleared once
// sent; the caller clears password. A password longer than MaxPassword fails, as the asking
// process would drop it.
func (r Request) Answer(password []byte) error {
	if len(password) > MaxPassword {
		return fmt.Errorf("the password is longer than %d bytes", MaxPassword)
	}
	reply := make([]byte, 0, 1+len(password))
	reply = append(append(reply, '+'), password...)
	defer clear(reply)
	return r.send(reply)
}

// Cancel tells the request that no password comes.
func (r Request) Cancel() error { return r.send([]byte("-")) }

func (r Request) send(reply []byte) error {
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: r.Socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(reply)
	return err
}

// monotonic is CLOCK_MONOTONIC, the clock of NotAfter.
func monotonic() time.Duration {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0
	}
	return time.Duration(ts.Nano())
}

// running reports whether the process runs; one of another user, which kill may not signal,
// runs too.
func running(pid int) bool {
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}
