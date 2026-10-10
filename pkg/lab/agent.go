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

// guestAgent is a connection to the QEMU guest agent of a VM with GuestAgent.
type guestAgent struct {
	conn net.Conn
	enc  *json.Encoder
	dec  *json.Decoder
}

// agentResponse is what the agent answers a request with.
type agentResponse struct {
	Return json.RawMessage `json:"return"`
	Error  *struct {
		Desc string `json:"desc"`
	} `json:"error"`
}

// dialGuestAgent connects to the guest agent and synchronises with it, until ctx ends. A request
// to an agent that does not run stays queued on its channel, so ctx should bound the wait.
func (vm *VM) dialGuestAgent(ctx context.Context) (*guestAgent, func(), error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", vm.Config.GuestAgentSocket())
	if err != nil {
		return nil, nil, fmt.Errorf("reach the guest agent: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Now()) })
	closeAgent := func() {
		stop()
		conn.Close()
	}
	a := &guestAgent{conn: conn, enc: json.NewEncoder(conn), dec: json.NewDecoder(conn)}
	// The agent answers requests in turn, so answers to an earlier client's requests may come
	// first; guest-sync's ID tells this one's apart.
	id := rand.Int64N(1 << 50)
	want := fmt.Sprint(id)
	got, err := a.call("guest-sync", map[string]any{"id": id})
	for err == nil && string(got) != want {
		var resp agentResponse
		err = a.dec.Decode(&resp)
		got = resp.Return
	}
	if err != nil {
		closeAgent()
		return nil, nil, fmt.Errorf("guest agent: guest-sync: %w", err)
	}
	return a, closeAgent, nil
}

// send sends a request to the agent.
func (a *guestAgent) send(command string, args any) error {
	req := map[string]any{"execute": command}
	if args != nil {
		req["arguments"] = args
	}
	return a.enc.Encode(req)
}

// call sends a request to the agent and returns its answer.
func (a *guestAgent) call(command string, args any) (json.RawMessage, error) {
	if err := a.send(command, args); err != nil {
		return nil, err
	}
	var resp agentResponse
	if err := a.dec.Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, errors.New(resp.Error.Desc)
	}
	return resp.Return, nil
}

// PingGuestAgent asks the QEMU guest agent of a VM with GuestAgent whether it answers, until ctx
// ends.
func (vm *VM) PingGuestAgent(ctx context.Context) error {
	a, closeAgent, err := vm.dialGuestAgent(ctx)
	if err != nil {
		return err
	}
	defer closeAgent()
	if _, err := a.call("guest-ping", nil); err != nil {
		return fmt.Errorf("guest agent: guest-ping: %w", err)
	}
	return nil
}

// shutdownAnswer is how long ShutdownGuest waits for the agent to say it cannot shut the guest
// down: it answers only then.
const shutdownAnswer = 5 * time.Second

// ShutdownGuest asks the QEMU guest agent of a VM with GuestAgent to power the guest off, as a
// hypervisor's shutdown does. QEMU exits once the guest is off.
func (vm *VM) ShutdownGuest(ctx context.Context) error {
	a, closeAgent, err := vm.dialGuestAgent(ctx)
	if err != nil {
		return err
	}
	defer closeAgent()
	if err := a.send("guest-shutdown", nil); err != nil {
		return fmt.Errorf("guest agent: guest-shutdown: %w", err)
	}
	a.conn.SetReadDeadline(time.Now().Add(shutdownAnswer))
	var resp agentResponse
	if err := a.dec.Decode(&resp); err == nil && resp.Error != nil {
		return fmt.Errorf("guest agent: guest-shutdown: %s", resp.Error.Desc)
	}
	return nil
}
