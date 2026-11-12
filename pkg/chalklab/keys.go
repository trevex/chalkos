package chalklab

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/trevex/chalkos/pkg/secureboot"
)

// createKeys creates the lab's Secure Boot keys, PK, KEK and db, in dir, as chalkctl secureboot
// new does, and a firmware variable store with them enrolled and Secure Boot on, from the
// firmware's template. They are test keys of this lab alone; the private keys are its owner's to
// read only.
func (a *app) createKeys(ctx context.Context, dir, labName, templateVars string) error {
	if err := secureboot.NewKeys(dir, "chalklab "+labName); err != nil {
		return err
	}
	keys, err := secureboot.LoadKeys(dir)
	if err != nil {
		return err
	}
	owner := secureboot.GUIDString(keys.Owner)
	if _, err := a.output(ctx, "virt-fw-vars", "--loglevel", "warning", "--input", templateVars, "--output", filepath.Join(dir, "OVMF_VARS.fd"),
		"--set-pk", owner, filepath.Join(dir, "PK.crt"),
		"--add-kek", owner, filepath.Join(dir, "KEK.crt"),
		"--add-db", owner, filepath.Join(dir, "db.crt"),
		"--secure-boot"); err != nil {
		return fmt.Errorf("enroll the lab's keys: %w", err)
	}
	return nil
}
