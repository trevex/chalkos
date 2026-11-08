package chalkd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/anypb"

	checkv1 "github.com/trevex/chalkos/pkg/api/check/v1"
	"github.com/trevex/chalkos/pkg/chalkd/layer"
	"github.com/trevex/chalkos/pkg/check"
	"github.com/trevex/chalkos/pkg/config"
	"github.com/trevex/chalkos/pkg/contract"
	"github.com/trevex/chalkos/pkg/imagecheck"
	"github.com/trevex/chalkos/pkg/outputs"
)

// Check is chalkd check: the image checks config for itself, as the chalkd of a running image
// asks it to before the image boots. It reads a chalkos.check.v1 Input on stdin and writes the
// Result to stdout, both as protobuf JSON, and asks for facts on the Unix socket at socket.
// system is the image's NixOS system, whose etc holds what it checks config with: its schemas,
// validators, outputs and layer facts, never the /etc of the system it runs on, which is the
// running image's. Validators, renderers and checks run as children of the checker, in the
// sandbox it runs in, and keep their files below dir, which the checker removes after.
//
// A refusal is part of the result; so is a fault of the image, such as schemas it cannot load,
// as its error. An error of Check itself is one of its input or output.
func (s *Server) Check(ctx context.Context, system, socket, dir string, stdin io.Reader, stdout io.Writer) error {
	data, err := io.ReadAll(stdin)
	if err != nil {
		return fmt.Errorf("read the input: %w", err)
	}
	in := &checkv1.Input{}
	if err := protojson.Unmarshal(data, in); err != nil {
		return fmt.Errorf("the input: %w", err)
	}
	res := s.checkInput(ctx, system, dir, in, imagecheck.DialFacts(ctx, socket))
	out, err := protojson.Marshal(res)
	if err != nil {
		return err
	}
	_, err = stdout.Write(out)
	return err
}

// checkInput checks in with what the image at system holds.
func (s *Server) checkInput(ctx context.Context, system, dir string, in *checkv1.Input, f *imagecheck.RemoteFacts) *checkv1.Result {
	img, err := s.systemImage(system, in.GetPlatform(), dir)
	if err != nil {
		return &checkv1.Result{Error: err.Error()}
	}
	delivered, err := imagecheck.DecodeRedacted(in.GetDelivered())
	if err != nil {
		return &checkv1.Result{Error: fmt.Sprintf("the delivered documents: %v", err)}
	}
	old, err := imagecheck.DecodeRedacted(in.GetOld())
	if err != nil {
		return &checkv1.Result{Error: fmt.Sprintf("the old documents: %v", err)}
	}
	res, err := check.Run(ctx, img, check.Input{Node: in.GetNode(), Delivered: delivered, Old: old, Facts: f.For})
	var refusal *check.Refusal
	switch {
	case errors.As(err, &refusal):
		r := &checkv1.Refusal{Stage: string(refusal.Stage), Message: refusal.Message}
		for _, f := range refusal.Failures {
			r.Failures = append(r.Failures, f.String())
		}
		return &checkv1.Result{Refusal: r, Identity: string(res.Identity)}
	case err != nil:
		return &checkv1.Result{Error: err.Error()}
	}
	// The image renders its outputs again at its boot.
	if res.Staged != nil {
		res.Staged.Discard()
	}
	docs, err := imagecheck.Encode(res.Documents)
	if err != nil {
		return &checkv1.Result{Error: err.Error()}
	}
	return &checkv1.Result{Documents: docs, Identity: string(res.Identity), Warnings: res.Warnings}
}

// systemImage is what the image whose NixOS system is system checks config with, read from its
// etc: its schemas and their rules, its validators and outputs, run and rendered below dir, and
// the target its layers' facts give, on platform. It plans no effects: an image that does not
// run has none to take.
func (s *Server) systemImage(system, platform, dir string) (check.Image, error) {
	c, err := contract.Load(filepath.Join(system, contract.DefaultDir))
	if err != nil {
		return check.Image{}, err
	}
	set, err := c.Rules()
	if err != nil {
		return check.Image{}, fmt.Errorf("the image's rules: %w", err)
	}
	facts, err := imageFacts(layer.Compiled[layer.ImageFacts](s.Plugins), func(h layer.ImageFacts) (*anypb.Any, error) {
		return h.SystemFacts(func(path string) ([]byte, error) { return os.ReadFile(filepath.Join(system, path)) })
	})
	if err != nil {
		return check.Image{}, err
	}
	t := config.Target{Platform: platform}
	if err := s.target(facts, &t); err != nil {
		return check.Image{}, err
	}
	render := outputs.Renderer{Root: dir, Run: outputs.Direct{}, KeepOwner: true}
	return check.Image{
		Schemas: c.Schemas, Target: t, ChangeRules: set, Outputs: c.Outputs, Renderer: render,
		Validators: check.Programs{Defs: c.Validators, Render: render},
	}, nil
}
