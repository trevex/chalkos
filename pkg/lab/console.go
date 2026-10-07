// Package lab runs chalkos images in QEMU virtual machines for tests and development clusters.
package lab

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
)

// Console collects the lines a VM writes to its serial port and lets tests wait for them.
type Console struct {
	mu     sync.Mutex
	cond   *sync.Cond
	lines  []string
	cursor int
	closed bool
}

// NewConsole reads lines from r until EOF. Each line is also written to log when log is non-nil.
func NewConsole(r io.Reader, log io.Writer) *Console {
	c := &Console{}
	c.cond = sync.NewCond(&c.mu)
	go c.read(r, log)
	return c
}

func (c *Console) read(r io.Reader, log io.Writer) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if log != nil {
			fmt.Fprintln(log, line)
		}
		c.mu.Lock()
		c.lines = append(c.lines, line)
		c.cond.Broadcast()
		c.mu.Unlock()
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

// Skip discards the lines read so far, so the next WaitFor sees only later output, such as the
// output of the next boot.
func (c *Console) Skip() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cursor = len(c.lines)
}
