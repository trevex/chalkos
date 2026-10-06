package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// QMP is a client for the QEMU Machine Protocol.
type QMP struct {
	mu   sync.Mutex
	conn net.Conn
	dec  *json.Decoder
}

// DialQMP connects to a QMP socket, retrying until ctx expires because QEMU creates the
// socket shortly after it starts.
func DialQMP(ctx context.Context, path string) (*QMP, error) {
	var d net.Dialer
	for {
		conn, err := d.DialContext(ctx, "unix", path)
		if err == nil {
			q := &QMP{conn: conn, dec: json.NewDecoder(conn)}
			if err := q.handshake(); err != nil {
				conn.Close()
				return nil, err
			}
			return q, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("dial qmp %s: %w", path, err)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (q *QMP) handshake() error {
	var greeting struct {
		QMP json.RawMessage `json:"QMP"`
	}
	if err := q.dec.Decode(&greeting); err != nil {
		return fmt.Errorf("qmp greeting: %w", err)
	}
	if greeting.QMP == nil {
		return fmt.Errorf("qmp greeting: missing QMP object")
	}
	_, err := q.Execute("qmp_capabilities", nil)
	return err
}

// Execute runs one QMP command and returns its "return" value.
func (q *QMP) Execute(command string, args any) (json.RawMessage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	req := map[string]any{"execute": command}
	if args != nil {
		req["arguments"] = args
	}
	if err := json.NewEncoder(q.conn).Encode(req); err != nil {
		return nil, fmt.Errorf("qmp %s: %w", command, err)
	}
	for {
		var resp struct {
			Return json.RawMessage `json:"return"`
			Error  *struct {
				Class string `json:"class"`
				Desc  string `json:"desc"`
			} `json:"error"`
			Event string `json:"event"`
		}
		if err := q.dec.Decode(&resp); err != nil {
			return nil, fmt.Errorf("qmp %s: %w", command, err)
		}
		if resp.Event != "" {
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("qmp %s: %s: %s", command, resp.Error.Class, resp.Error.Desc)
		}
		return resp.Return, nil
	}
}

// Close closes the connection.
func (q *QMP) Close() error {
	return q.conn.Close()
}
