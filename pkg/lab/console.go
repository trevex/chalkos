// Package lab runs chalkos images in QEMU virtual machines for tests and development clusters.
package lab

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Console collects the lines a VM writes to its serial port and lets tests wait for them.
type Console struct {
	mu     sync.Mutex
	cond   *sync.Cond
	lines  []string
	cursor int
	closed bool
	// read is how far into the console's log the lines read reach, and skipTo the offset Skip
	// saw the log end at: lines read later that end before it are skipped too. size returns the
	// log's size; nil for a console that is no file.
	read, skipTo int64
	size         func() int64
}

// NewConsole reads lines from r until EOF. Each line is also written to log when log is non-nil.
func NewConsole(r io.Reader, log io.Writer) *Console {
	return newConsole(r, log, 0, nil)
}

func newConsole(r io.Reader, log io.Writer, from int64, size func() int64) *Console {
	c := &Console{read: from, size: size}
	c.cond = sync.NewCond(&c.mu)
	go c.scan(r, log)
	return c
}

// FollowConsole follows a VM's console log from its start, as the QEMU of the VM appends to it,
// without reaching the VM itself, until stop is called.
func FollowConsole(path string) (c *Console, stop func(), err error) {
	c, fr, err := followConsole(path, 0)
	if err != nil {
		return nil, nil, err
	}
	return c, func() { fr.Close() }, nil
}

// followConsole follows the console log QEMU appends to from the offset given, until the
// follower is closed.
func followConsole(path string, from int64) (*Console, *follower, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		f.Close()
		return nil, nil, err
	}
	fr := &follower{f: f, closed: make(chan struct{})}
	size := func() int64 {
		info, err := os.Stat(path)
		if err != nil {
			return 0
		}
		return info.Size()
	}
	return newConsole(fr, nil, from, size), fr, nil
}

// follower reads a file another process appends to as far as it is written, until closed.
type follower struct {
	f         *os.File
	closed    chan struct{}
	closeOnce sync.Once
}

func (r *follower) Read(p []byte) (int, error) {
	for {
		n, err := r.f.Read(p)
		if n > 0 || err != nil && err != io.EOF {
			return n, err
		}
		select {
		case <-r.closed:
			return 0, io.EOF
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Close ends the reads; the file is closed once the reader returned.
func (r *follower) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}

// scanLines splits lines keeping their ends, so the console counts the bytes it read.
func scanLines(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func (c *Console) scan(r io.Reader, log io.Writer) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	sc.Split(scanLines)
	for sc.Scan() {
		raw := sc.Text()
		line := strings.TrimRight(raw, "\r\n")
		if log != nil {
			fmt.Fprintln(log, line)
		}
		c.mu.Lock()
		c.read += int64(len(raw))
		c.lines = append(c.lines, line)
		if c.read <= c.skipTo {
			c.cursor = len(c.lines)
		}
		c.cond.Broadcast()
		c.mu.Unlock()
	}
	if f, ok := r.(*follower); ok {
		f.f.Close()
	}
	c.mu.Lock()
	c.closed = true
	c.cond.Broadcast()
	c.mu.Unlock()
}

// WaitFor blocks until a line after the previous match matches re and returns its submatches.
// Lines are consumed in order, so consecutive calls see consecutive output.
func (c *Console) WaitFor(ctx context.Context, re *regexp.Regexp) ([]string, error) {
	stop := context.AfterFunc(ctx, func() {
		c.mu.Lock()
		c.cond.Broadcast()
		c.mu.Unlock()
	})
	defer stop()

	c.mu.Lock()
	defer c.mu.Unlock()
	for {
		for i := c.cursor; i < len(c.lines); i++ {
			if m := re.FindStringSubmatch(c.lines[i]); m != nil {
				c.cursor = i + 1
				return m, nil
			}
		}
		c.cursor = len(c.lines)
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("waiting for %q on console: %w", re, err)
		}
		if c.closed {
			return nil, fmt.Errorf("waiting for %q on console: console closed", re)
		}
		c.cond.Wait()
	}
}

// Skip discards the lines written so far, so the next WaitFor sees only later output, such as the
// output of the next boot.
func (c *Console) Skip() {
	var size int64
	if c.size != nil {
		size = c.size()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cursor = len(c.lines)
	c.skipTo = size
}
