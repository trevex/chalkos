package imagecheck

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
)

// Local runs a checker in this process, as tests do: Check is the checker's own function, the one
// chalkd check runs, which Local calls with the image's system where it lies on this machine,
// below the directory that holds Store, and with a facts socket of its own. The input and the
// result pass as protobuf JSON, as through the sandbox.
type Local struct {
	Check func(ctx context.Context, system, socket, dir string, stdin io.Reader, stdout io.Writer) error
}

func (l Local) Run(ctx context.Context, img Image, in *checkv1.Input, f Facts) (*checkv1.Result, error) {
	dir, err := os.MkdirTemp("", "chalkos-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ln, err := net.Listen("unix", filepath.Join(dir, "facts.sock"))
	if err != nil {
		return nil, err
	}
	stop := ServeFacts(ln, f)
	defer stop()
	input, err := protojson.Marshal(in)
	if err != nil {
		return nil, err
	}
	root := filepath.Dir(filepath.Dir(img.Store))
	system := filepath.Join(root, strings.TrimPrefix(img.System, "/"))
	var out bytes.Buffer
	if err := l.Check(ctx, system, filepath.Join(dir, "facts.sock"), filepath.Join(dir, "outputs"), bytes.NewReader(input), &out); err != nil {
		return nil, fmt.Errorf("the image's checker failed: %w", err)
	}
	res := &checkv1.Result{}
	if err := protojson.Unmarshal(out.Bytes(), res); err != nil {
		return nil, fmt.Errorf("the image's checker answered no result: %w", err)
	}
	return res, nil
}
