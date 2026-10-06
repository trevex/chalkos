package lab

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeQMP serves one connection: it sends the greeting, then answers each command
// with the next reply from replies, writing an event before every reply.
func fakeQMP(t *testing.T, replies ...string) (path string, commands chan string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "qmp.sock")
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	commands = make(chan string, len(replies))
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte(`{"QMP": {"version": {}, "capabilities": []}}` + "\n"))
		r := bufio.NewReader(conn)
		for _, reply := range replies {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			var req struct{ Execute string }
			json.Unmarshal([]byte(line), &req)
			commands <- req.Execute
			conn.Write([]byte(`{"event": "RESET", "data": {}}` + "\n"))
			conn.Write([]byte(reply + "\n"))
		}
	}()
	return path, commands
}

func TestQMPExecuteSkipsEvents(t *testing.T) {
	path, commands := fakeQMP(t, `{"return": {}}`, `{"return": {"status": "running"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q, err := DialQMP(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if got := <-commands; got != "qmp_capabilities" {
		t.Fatalf("first command = %q, want qmp_capabilities", got)
	}

	ret, err := q.Execute("query-status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ret), `"running"`) {
		t.Fatalf("return = %s, want status running", ret)
	}
}

func TestQMPExecuteReturnsErrors(t *testing.T) {
	path, _ := fakeQMP(t, `{"return": {}}`, `{"error": {"class": "GenericError", "desc": "boom"}}`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q, err := DialQMP(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if _, err := q.Execute("system_reset", nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want GenericError boom", err)
	}
}

func TestDialQMPWaitsForSocket(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := DialQMP(ctx, path); err == nil {
		t.Fatal("DialQMP succeeded without a server")
	}
}
