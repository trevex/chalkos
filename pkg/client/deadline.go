package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"connectrpc.com/connect/v2"
)

// CallTimeout bounds a unary call to a node whose context has no deadline of its own: long enough
// for a slow but healthy node, short enough that a node that accepted the call and never answers
// does not hold chalkctl until Ctrl-C. Streams keep their own handling.
const CallTimeout = 2 * time.Minute

type unboundedKey struct{}

// Unbounded marks ctx for calls that may run longer than CallTimeout, such as a config whose
// effects run on the node or a drain; only ctx itself then bounds them.
func Unbounded(ctx context.Context) context.Context {
	return context.WithValue(ctx, unboundedKey{}, true)
}

// errNoAnswer is the cause of a call's context that CallTimeout ended.
var errNoAnswer = errors.New("no answer within the call timeout")

// bounded gives a unary call CallTimeout when its context has no deadline and is not Unbounded,
// and reports a call it ended as the node not answering.
func (c *Conn) bounded(next connect.ClientFunc) connect.ClientFunc {
	return func(ctx context.Context, spec connect.Spec) (connect.ClientStream, error) {
		if _, ok := ctx.Deadline(); ok || c.callTimeout <= 0 || spec.StreamType != connect.StreamTypeUnary || ctx.Value(unboundedKey{}) != nil {
			return next(ctx, spec)
		}
		ctx, cancel := context.WithTimeoutCause(ctx, c.callTimeout, errNoAnswer)
		stream, err := next(ctx, spec)
		if err != nil {
			cancel()
			return nil, c.noAnswer(ctx, err)
		}
		return &boundedStream{ClientStream: stream, ctx: ctx, cancel: cancel, conn: c}, nil
	}
}

// noAnswer replaces err when CallTimeout ended the call's context.
func (c *Conn) noAnswer(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, io.EOF) || context.Cause(ctx) != errNoAnswer {
		return err
	}
	return connect.NewError(connect.CodeDeadlineExceeded, fmt.Sprintf("chalkd on %s did not answer within %s", c.endpoint, minutes(c.callTimeout))).WithCause(err)
}

// minutes formats d in whole minutes when it is some, such as 2m.
func minutes(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return d.String()
}

// boundedStream is a unary call's stream whose context CallTimeout bounds.
type boundedStream struct {
	connect.ClientStream
	ctx    context.Context
	cancel context.CancelFunc
	conn   *Conn
}

func (s *boundedStream) SendHeaders() error {
	return s.conn.noAnswer(s.ctx, s.ClientStream.SendHeaders())
}

func (s *boundedStream) Send(msg any) error {
	return s.conn.noAnswer(s.ctx, s.ClientStream.Send(msg))
}

func (s *boundedStream) CloseSend() error {
	return s.conn.noAnswer(s.ctx, s.ClientStream.CloseSend())
}

func (s *boundedStream) Receive(msg any) error {
	return s.conn.noAnswer(s.ctx, s.ClientStream.Receive(msg))
}

func (s *boundedStream) Close() error {
	defer s.cancel()
	return s.ClientStream.Close()
}
