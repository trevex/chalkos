package lab

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// TestFileConsoleSkip follows a console log from an offset, and Skip passes over what the log
// holds then, even lines not read yet.
func TestFileConsoleSkip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "console.log")
	if err := os.WriteFile(path, []byte("before\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, f, err := followConsole(path, int64(len("before\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	appendLine := func(line string) {
		log, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(log, line)
		log.Close()
	}
	appendLine("CHALKTEST a=1\r\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if m, err := c.WaitFor(ctx, factRE); err != nil || m[1] != "a" {
		t.Fatalf("first fact = %v, %v", m, err)
	}
	appendLine("CHALKTEST b=1\n")
	c.Skip()
	appendLine("CHALKTEST c=1\n")
	if m, err := c.WaitFor(ctx, factRE); err != nil || m[1] != "c" {
		t.Fatalf("fact after Skip = %v, %v; want c", m, err)
	}
	f.Close()
	if _, err := c.WaitFor(ctx, factRE); err == nil || !strings.Contains(err.Error(), "console closed") {
		t.Errorf("WaitFor after Close = %v", err)
	}
}
