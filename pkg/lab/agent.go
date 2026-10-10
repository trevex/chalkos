package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"time"
)

// PingGuestAgent asks the QEMU guest agent of a VM with GuestAgent whether it answers. A request
// to an agent that does not run stays queued on its channel, so ctx should bound the wait.
func (vm *VM) PingGuestAgent(ctx context.Context) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", vm.Config.GuestAgentSocket())
	if err != nil {
		return fmt.Errorf("reach the guest agent: %w", err)
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	defer stop()
	enc, dec := json.NewEncoder(conn), json.NewDecoder(conn)
	call := func(command string, args any) (json.RawMessage, error) {
		req := map[string]any{"execute": command}
		if args != nil {
			req["arguments"] = args
		}
		if err := enc.Encode(req); err != nil {
			return nil, err
		}
		var resp struct {
			Return json.RawMessage `json:"return"`
			Error  *struct {
				Desc string `json:"desc"`
			} `json:"error"`
		}
		if err := dec.Decode(&resp); err != nil {
			return nil, err
		}
		if resp.Error != nil {
			return nil, errors.New(resp.Error.Desc)
		}
		return resp.Return, nil
	}
	// The agent answers requests in turn, so answers to an earlier client's requests may come
	// first; guest-sync's ID tells this one's apart.
	id := rand.Int64N(1 << 50)
	want := fmt.Sprint(id)
	got, err := call("guest-sync", map[string]any{"id": id})
	for err == nil && string(got) != want {
		var resp struct {
			Return json.RawMessage `json:"return"`
		}
		err = dec.Decode(&resp)
		got = resp.Return
	}
	if err != nil {
		return fmt.Errorf("guest agent: guest-sync: %w", err)
	}
	if _, err := call("guest-ping", nil); err != nil {
		return fmt.Errorf("guest agent: guest-ping: %w", err)
	}
	return nil
}
