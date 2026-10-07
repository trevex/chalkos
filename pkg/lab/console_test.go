package lab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"testing"
	"time"
)

var factRE = regexp.MustCompile(`CHALKTEST (\w+)=(\S*)`)

func TestConsoleWaitForMatchesInOrder(t *testing.T) {
	r, w := io.Pipe()
	c := NewConsole(r, nil)
	go func() {
		fmt.Fprint(w, "boot\r\nCHALKTEST a=1\nnoise\nCHALKTEST b=2\n")
		w.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	m, err := c.WaitFor(ctx, factRE)
	if err != nil || m[1] != "a" || m[2] != "1" {
		t.Fatalf("first match = %v, %v; want a=1", m, err)
	}
	m, err = c.WaitFor(ctx, factRE)
	if err != nil || m[1] != "b" {
		t.Fatalf("second match = %v, %v; want b=2", m, err)
	}
	if _, err := c.WaitFor(ctx, factRE); err == nil {
		t.Fatal("third WaitFor succeeded after the console closed")
	}
}

func TestConsoleWaitForHonoursContext(t *testing.T) {
	r, _ := io.Pipe()
	c := NewConsole(r, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.WaitFor(ctx, factRE)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
}

func TestConsoleWritesLogWithoutCarriageReturns(t *testing.T) {
	r, w := io.Pipe()
	var log bytes.Buffer
	c := NewConsole(r, &log)
	go fmt.Fprint(w, "a\r\nb\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.WaitFor(ctx, regexp.MustCompile(`^b$`)); err != nil {
		t.Fatal(err)
	}
	if got := log.String(); got != "a\nb\n" {
		t.Fatalf("log = %q, want %q", got, "a\nb\n")
	}
}

func TestConsoleSkipDiscardsEarlierLines(t *testing.T) {
	r, w := io.Pipe()
	c := NewConsole(r, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	fmt.Fprint(w, "CHALKTEST boot=1\nCHALKTEST boot=2\n")
	waitForLines(t, c, 2)
	c.Skip()
	go func() {
		fmt.Fprint(w, "CHALKTEST boot=3\n")
		w.Close()
	}()
	m, err := c.WaitFor(ctx, factRE)
	if err != nil || m[2] != "3" {
		t.Fatalf("after Skip = %v, %v; want boot=3", m, err)
	}
}

// waitForLines waits until the console holds n lines.
func waitForLines(t *testing.T, c *Console, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		got := len(c.lines)
		c.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the console holds %d lines, want %d", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}
