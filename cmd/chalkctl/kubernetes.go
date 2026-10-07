package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"filippo.io/age"

	kpki "github.com/trevex/chalkos/pkg/kubernetes/pki"
	"github.com/trevex/chalkos/pkg/pki"
)

// kubernetesShare issues the share of a node whose role has Kubernetes; nil for one without.
func kubernetesShare(t *target, now time.Time) ([]byte, error) {
	role, ok := t.cluster.manifest.Roles[t.node.Role]
	if !ok {
		return nil, fmt.Errorf("the cluster has no role %s", t.node.Role)
	}
	if role.Kind == "" {
		return nil, nil
	}
	k, err := t.secrets.RequireKubernetes()
	if err != nil {
		return nil, err
	}
	share, err := kpki.ShareFor(k, role.Kind, t.name, now)
	if err != nil {
		return nil, err
	}
	return share.Encode()
}

func (a *app) secretsUpgrade(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("secrets upgrade", flag.ContinueOnError)
	var sf secretFlags
	sf.register(fs)
	flake := fs.String("flake", ".", "directory of the flake, where the secrets file is looked up")
	var recipients stringList
	fs.Var(&recipients, "recipient", "age recipient to encrypt the new file to; may be repeated")
	plaintext := fs.Bool("plaintext", false, "write the new file unencrypted")
	out := fs.String("out", "", "file to write the upgraded secrets to; it must not exist")
	publicOut := fs.String("public-out", "", "also write the public half, like secrets.pub.json, to this file")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("secrets upgrade: --out is required; the secrets file itself is never rewritten")
	}
	if (len(recipients) == 0) == !*plaintext {
		return errors.New("secrets upgrade: pass --recipient (one or more) or --plaintext")
	}
	for _, p := range []string{*out, *publicOut} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s exists; secrets upgrade writes a new file", p)
		}
	}
	old, err := a.loadSecrets(ctx, sf, *flake)
	if err != nil {
		return err
	}
	secrets, err := pki.Upgrade(old, time.Now())
	if err != nil {
		return err
	}
	var data []byte
	if *plaintext {
		data, err = secrets.Encode()
	} else {
		var parsed []age.Recipient
		for _, r := range recipients {
			rec, err := pki.ParseRecipient(r, a.pluginUI())
			if err != nil {
				return err
			}
			parsed = append(parsed, rec)
		}
		data, err = secrets.Encrypt(parsed...)
	}
	if err != nil {
		return err
	}
	if err := writeNew(*out, data, 0o600); err != nil {
		return err
	}
	if *publicOut != "" {
		public, err := json.MarshalIndent(secrets.Public(), "", "  ")
		if err != nil {
			return err
		}
		if err := writeNew(*publicOut, append(public, '\n'), 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.stdout, "wrote %s with the Kubernetes secrets; the OS CA, the admin certificate and the recovery secret are unchanged\n", *out)
	return nil
}
