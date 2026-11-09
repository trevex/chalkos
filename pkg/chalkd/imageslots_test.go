package chalkd

import (
	"context"
	"io"
	"testing"

	"github.com/trevex/chalkos/pkg/upgrade"
)

// fakeSlots stand in for the boot disk's image slots. Each call goes to the function set for it,
// or else to the boot disk's slots, as a Server without fakeSlots has them.
type fakeSlots struct {
	t          *testing.T
	disk       bootDisk
	install    func(ctx context.Context, h upgrade.Header, config, secret string, stageOnly bool, image io.Reader) (upgrade.Result, imageConfig, error)
	activate   func(ctx context.Context, rootHash []byte) (upgrade.Result, error)
	reactivate func(ctx context.Context, rootHash []byte, record func() error) (upgrade.Result, error)
	read       func(ctx context.Context, rootHash []byte, read func(imageStore, string) error) error
}

// fakeImageSlots are s's fakeSlots, which it is given unless it has them already.
func fakeImageSlots(t *testing.T, s *Server) *fakeSlots {
	if f, ok := s.slots.(*fakeSlots); ok {
		return f
	}
	f := &fakeSlots{t: t, disk: bootDisk{s}}
	s.slots = f
	return f
}

// installs has each install read the whole image and answer res and err.
func (f *fakeSlots) installs(res upgrade.Result, err error) {
	f.install = func(_ context.Context, _ upgrade.Header, _, _ string, _ bool, r io.Reader) (upgrade.Result, imageConfig, error) {
		if _, rerr := io.Copy(io.Discard, r); rerr != nil {
			f.t.Error(rerr)
		}
		return res, imageConfig{}, err
	}
}

// forbidChanges fails the test on an install, an activation or a re-activation.
func (f *fakeSlots) forbidChanges() {
	f.install = func(_ context.Context, h upgrade.Header, _, _ string, _ bool, _ io.Reader) (upgrade.Result, imageConfig, error) {
		f.t.Errorf("installed the image %x", h.RootHash)
		return upgrade.Result{}, imageConfig{}, nil
	}
	f.activate = func(_ context.Context, rootHash []byte) (upgrade.Result, error) {
		f.t.Errorf("activated the image %x", rootHash)
		return upgrade.Result{}, nil
	}
	f.reactivate = func(_ context.Context, rootHash []byte, _ func() error) (upgrade.Result, error) {
		f.t.Errorf("re-activated the image %x", rootHash)
		return upgrade.Result{}, nil
	}
}

func (f *fakeSlots) Install(ctx context.Context, h upgrade.Header, config, secret string, stageOnly bool, image io.Reader) (upgrade.Result, imageConfig, error) {
	if f.install == nil {
		return f.disk.Install(ctx, h, config, secret, stageOnly, image)
	}
	return f.install(ctx, h, config, secret, stageOnly, image)
}

func (f *fakeSlots) Activate(ctx context.Context, rootHash []byte) (upgrade.Result, error) {
	if f.activate == nil {
		return f.disk.Activate(ctx, rootHash)
	}
	return f.activate(ctx, rootHash)
}

func (f *fakeSlots) Reactivate(ctx context.Context, rootHash []byte, record func() error) (upgrade.Result, error) {
	if f.reactivate == nil {
		return f.disk.Reactivate(ctx, rootHash, record)
	}
	return f.reactivate(ctx, rootHash, record)
}

func (f *fakeSlots) Read(ctx context.Context, rootHash []byte, read func(imageStore, string) error) error {
	if f.read == nil {
		return f.disk.Read(ctx, rootHash, read)
	}
	return f.read(ctx, rootHash, read)
}
