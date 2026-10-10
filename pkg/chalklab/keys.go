package chalklab

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

// createKeys creates the lab's Secure Boot keys, PK, KEK and db, in dir, and a firmware variable
// store with them enrolled and Secure Boot on, from the firmware's template. They are test keys of
// this lab alone; the private keys are its owner's to read only.
func (a *app) createKeys(ctx context.Context, dir, cluster, templateVars string) error {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	now := time.Now()
	for _, name := range []string{"PK", "KEK", "db"} {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
		if err != nil {
			return err
		}
		tmpl := &x509.Certificate{
			SerialNumber:          serial,
			Subject:               pkix.Name{CommonName: fmt.Sprintf("chalklab %s %s", cluster, name)},
			NotBefore:             now.Add(-time.Hour),
			NotAfter:              now.AddDate(10, 0, 0),
			KeyUsage:              x509.KeyUsageDigitalSignature,
			BasicConstraintsValid: true,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			return err
		}
		pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return err
		}
		if err := writeNew(filepath.Join(dir, name+".key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600); err != nil {
			return err
		}
		if err := writeNew(filepath.Join(dir, name+".crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
			return err
		}
	}
	owner, err := randomGUID()
	if err != nil {
		return err
	}
	if _, err := a.output(ctx, "virt-fw-vars", "--loglevel", "warning", "--input", templateVars, "--output", filepath.Join(dir, "OVMF_VARS.fd"),
		"--set-pk", owner, filepath.Join(dir, "PK.crt"),
		"--add-kek", owner, filepath.Join(dir, "KEK.crt"),
		"--add-db", owner, filepath.Join(dir, "db.crt"),
		"--secure-boot"); err != nil {
		return fmt.Errorf("enroll the lab's keys: %w", err)
	}
	return nil
}

// writeNew writes a file that must not exist yet with the permissions given.
func writeNew(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// randomGUID is a random GUID, the owner of the keys the lab enrolls.
func randomGUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
