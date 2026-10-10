package chalkctl

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"filippo.io/age"
	"filippo.io/age/plugin"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/trevex/chalkos/pkg/pki"
)

// stringList is a flag that may be given several times.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }
func (l *stringList) Type() string       { return "strings" }

// secretFlags say where the secrets file is and how to decrypt it.
type secretFlags struct {
	path       string
	identities stringList
}

func (s *secretFlags) register(fs *pflag.FlagSet) {
	fs.StringVar(&s.path, "secrets", "", "secrets file, - for standard input (default secrets.age, else secrets.json, in the flake directory)")
	fs.Var(&s.identities, "identity", "age identity file to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)")
}

// loadSecrets reads the secrets file in whatever format it has. Age identities are read only when
// the file is encrypted.
func (a *app) loadSecrets(ctx context.Context, s secretFlags, flake string) (pki.Secrets, error) {
	path, _, err := secretsPath(s, flake)
	if err != nil {
		return pki.Secrets{}, err
	}
	var data []byte
	if path == "-" {
		data, err = io.ReadAll(a.stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return pki.Secrets{}, fmt.Errorf("read the secrets file: %w", err)
	}
	secrets, err := pki.ReadSecrets(data, func() ([]age.Identity, error) { return a.ageIdentities(ctx, s.identities) })
	if err != nil {
		return pki.Secrets{}, fmt.Errorf("%s: %w", path, err)
	}
	return secrets, nil
}

// secretsPath is the secrets file --secrets names, else secrets.age, else secrets.json, in the
// flake directory; found says whether it was given or exists.
func secretsPath(s secretFlags, flake string) (path string, found bool, err error) {
	if s.path != "" {
		return s.path, true, nil
	}
	for _, name := range []string{"secrets.age", "secrets.json"} {
		path = filepath.Join(flake, name)
		switch _, err := os.Stat(path); {
		case err == nil:
			return path, true, nil
		case !errors.Is(err, fs.ErrNotExist):
			return "", false, err
		}
	}
	return path, false, nil
}

// ageIdentities parses the given identity files, which must all load, or the default ones that
// exist, skipping those that do not load.
func (a *app) ageIdentities(ctx context.Context, files []string) ([]age.Identity, error) {
	var ids []age.Identity
	if len(files) > 0 {
		for _, f := range files {
			parsed, err := a.parseIdentity(ctx, f)
			if err != nil {
				return nil, err
			}
			ids = append(ids, parsed...)
		}
		return ids, nil
	}
	for _, f := range []string{
		filepath.Join(a.home, ".config", "chalkos", "age.key"),
		filepath.Join(a.home, ".ssh", "id_ed25519"),
		filepath.Join(a.home, ".ssh", "id_rsa"),
	} {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		parsed, err := a.parseIdentity(ctx, f)
		if err != nil {
			// A default key may be one age cannot use, such as an encrypted SSH key in the old
			// format without its .pub; another one may still decrypt the file.
			fmt.Fprintf(a.stderr, "chalkctl: warning: skipping %v\n", err)
			continue
		}
		ids = append(ids, parsed...)
	}
	if len(ids) == 0 {
		return nil, errors.New("the secrets file is encrypted and no usable age identity was found; pass --identity")
	}
	return ids, nil
}

// parseIdentity parses an identity file. Its errors name the file, never its contents.
func (a *app) parseIdentity(ctx context.Context, file string) ([]age.Identity, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	ids, err := pki.ParseIdentities(data, a.pluginUI(), a.passphrase(ctx, file), func() ([]byte, error) {
		return os.ReadFile(file + ".pub")
	})
	if err != nil {
		return nil, fmt.Errorf("identity %s: %w", file, err)
	}
	return ids, nil
}

func (a *app) pluginUI() *plugin.ClientUI {
	return plugin.NewTerminalUI(func(format string, v ...any) {
		fmt.Fprintf(a.stderr, "chalkctl: "+format+"\n", v...)
	}, func(format string, v ...any) {
		fmt.Fprintf(a.stderr, "chalkctl: warning: "+format+"\n", v...)
	})
}

// passphrase asks for an SSH key's passphrase on the terminal.
func (a *app) passphrase(ctx context.Context, file string) pki.SSHPassphrase {
	return func() ([]byte, error) {
		pass, err := a.readSecret(ctx, fmt.Sprintf("Passphrase for %s: ", file))
		if errors.Is(err, errNoTerminal) {
			return nil, fmt.Errorf("%s is encrypted and there is no terminal to ask for its passphrase", file)
		}
		return pass, err
	}
}

var (
	errNoTerminal  = errors.New("there is no terminal")
	errInterrupted = errors.New("interrupted")
)

// ttySecret asks for a secret on the terminal without echoing it.
func ttySecret(ctx context.Context, prompt string) ([]byte, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, errNoTerminal
	}
	defer tty.Close()
	fd := int(tty.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return nil, errNoTerminal
	}
	fmt.Fprint(tty, prompt)
	secret, err := readInterruptible(ctx, func() ([]byte, error) { return term.ReadPassword(fd) }, func() { term.Restore(fd, state) })
	fmt.Fprintln(tty)
	return secret, err
}

// readInterruptible returns what read returns, or errInterrupted once ctx ends. chalkctl handles
// SIGINT, so Ctrl-C does not end term.ReadPassword, which cannot be cancelled either: the read is
// abandoned and restore turns echo back on.
func readInterruptible(ctx context.Context, read func() ([]byte, error), restore func()) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := read()
		done <- result{data, err}
	}()
	select {
	case r := <-done:
		return r.data, r.err
	case <-ctx.Done():
		restore()
		return nil, errInterrupted
	}
}

func (a *app) recoveryKeyCommand() *cobra.Command {
	var cf clusterFlags
	var sf secretFlags
	cmd := a.command(&cobra.Command{
		Use:   "recovery-key <node>",
		Short: "Print a node's recovery key",
	}, func(a *app, ctx context.Context, pos []string) error { return a.recoveryKey(ctx, cf, sf, pos) })
	cf.register(cmd.Flags())
	sf.register(cmd.Flags())
	return cmd
}

func (a *app) recoveryKey(ctx context.Context, cf clusterFlags, sf secretFlags, pos []string) error {
	if len(pos) != 1 {
		return errors.New("usage: chalkctl recovery-key <node>")
	}
	c, err := a.loadCluster(ctx, cf)
	if err != nil {
		return err
	}
	if _, err := c.node(pos[0]); err != nil {
		return err
	}
	secrets, err := a.loadSecrets(ctx, sf, cf.flake)
	if err != nil {
		return err
	}
	key, err := pki.RecoveryKey(secrets.RecoverySecret, c.manifest.Cluster.Name, pos[0])
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, key)
	return nil
}

type genSecretsFlags struct {
	recipients stringList
	plaintext  bool
	out        string
}

func (a *app) genSecretsCommand() *cobra.Command {
	var f genSecretsFlags
	cmd := a.command(&cobra.Command{
		Use:   "secrets",
		Short: "Generate the cluster's secrets file",
	}, func(a *app, ctx context.Context, pos []string) error { return a.genSecrets(f) })
	fs := cmd.Flags()
	fs.Var(&f.recipients, "recipient", "age recipient to encrypt secrets.age to: an age public key, an SSH public key, or an age plugin recipient; may be repeated")
	fs.BoolVar(&f.plaintext, "plaintext", false, "write secrets.json unencrypted, for files protected by other means")
	fs.StringVar(&f.out, "out", ".", "directory to write the files to")
	return cmd
}

func (a *app) genSecrets(f genSecretsFlags) error {
	recipients := f.recipients
	if (len(recipients) == 0) == !f.plaintext {
		return errors.New("gen secrets: pass --recipient (one or more) or --plaintext")
	}
	name := "secrets.age"
	if f.plaintext {
		name = "secrets.json"
	}
	secretsFile := filepath.Join(f.out, name)
	publicPath := filepath.Join(f.out, "secrets.pub.json")
	// The secrets file is written once: a new one would orphan every installed node.
	for _, p := range []string{filepath.Join(f.out, "secrets.age"), filepath.Join(f.out, "secrets.json"), publicPath} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s exists; the secrets are generated once per cluster", p)
		}
	}

	secrets, err := pki.GenerateSecrets(time.Now())
	if err != nil {
		return err
	}
	if err := a.writeSecrets(secrets, recipients, f.plaintext, secretsFile, publicPath); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "wrote %s and %s\n", secretsFile, publicPath)
	if f.plaintext {
		fmt.Fprintf(a.stderr, "warning: %s holds the cluster's secrets unencrypted; protect it by other means, for example keep it out of version control with a .gitignore entry\n", secretsFile)
	}
	return nil
}

// writeNew writes a file that must not exist yet.
func writeNew(path string, data []byte, perm fs.FileMode) error {
	return createNew(path, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// createNew creates a file that must not exist yet with what write writes. It removes the file
// again when it cannot be written completely: a partial secrets file would read as corrupt, and
// would stop the next run from generating the file.
func createNew(path string, perm fs.FileMode, write func(w io.Writer) error) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	err = write(f)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// writeSecrets writes the secrets file, encrypted to the recipients, which it records inside it,
// unless plaintext, and its public half when publicPath is set. Neither may exist: a secrets file
// is never overwritten.
func (a *app) writeSecrets(secrets pki.Secrets, recipients []string, plaintext bool, path, publicPath string) error {
	for _, p := range []string{path, publicPath} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s exists; secrets are always written to a new file", p)
		}
	}
	var data []byte
	var err error
	if plaintext {
		data, err = secrets.Encode()
	} else {
		var parsed []age.Recipient
		if parsed, err = a.ageRecipients(recipients); err != nil {
			return err
		}
		secrets.Recipients = recipients
		data, err = secrets.Encrypt(parsed...)
	}
	if err != nil {
		return err
	}
	if err := writeNew(path, data, 0o600); err != nil {
		return err
	}
	if !plaintext {
		a.sayRecipients(path, recipients)
	}
	if publicPath == "" {
		return nil
	}
	public, err := encodePublic(secrets)
	if err != nil {
		return err
	}
	return writeNew(publicPath, public, 0o644)
}

// sayRecipients names whom an encrypted secrets file was encrypted to, so an operator notices a
// recipient that does not belong.
func (a *app) sayRecipients(path string, recipients []string) {
	fmt.Fprintf(a.stdout, "encrypted %s to %s\n", path, strings.Join(recipients, ", "))
}

// encodePublic encodes the public half of the secrets.
func encodePublic(secrets pki.Secrets) ([]byte, error) {
	data, err := json.MarshalIndent(secrets.Public(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// changeFlags are the flags of commands that change the secrets file.
type changeFlags struct {
	out, publicOut string
	recipients     stringList
}

func (c *changeFlags) register(fs *pflag.FlagSet) {
	fs.StringVar(&c.out, "out", "", "write the changed secrets file to this new file instead of updating the secrets file in place")
	fs.StringVar(&c.publicOut, "public-out", "", "write the public half to this new file instead of updating secrets.pub.json beside the secrets file in place")
	fs.Var(&c.recipients, "recipient", "age recipient to encrypt the changed secrets file to instead of those it records; may be repeated. A plaintext secrets file is encrypted only to a new .age file given with --out")
}

// secretsFile is a secrets file a command changes, with where and how the changes are written:
// back to the file in place, keeping its previous version as <file>.prev, or to new files; in the
// format the file had, an encrypted file to the recipients it records.
type secretsFile struct {
	secrets pki.Secrets
	// path is the file the secrets were read from, out the one they are written to.
	path, out string
	// publicOut receives the public half; empty leaves it alone.
	publicOut  string
	format     pki.Format
	recipients []string
	// outNew and publicNew are set while out and publicOut are new files that were not written
	// yet; once written, later writes replace them in place.
	outNew, publicNew bool
	// lock holds the secrets file's lock until Close; nil for standard input.
	lock *os.File
	// sum is the SHA-256 of what the file held when it was read or last written, so a change by
	// anything but this command is noticed before it is overwritten.
	sum [sha256.Size]byte
}

// Close releases the secrets file's lock.
func (f *secretsFile) Close() error {
	if f.lock == nil {
		return nil
	}
	err := f.lock.Close()
	f.lock = nil
	return err
}

// inPlace reports whether the changes go back to the file they were read from.
func (f *secretsFile) inPlace() bool { return f.out == f.path }

// openSecrets reads the secrets file for a command that changes it, and works out where and how
// the changes are written before anything is changed. The recipients come from inside the
// encrypted file, or from --recipient, never from secrets.pub.json, which anyone who can write
// beside the secrets file could change.
func (a *app) openSecrets(ctx context.Context, s secretFlags, flake string, c changeFlags) (*secretsFile, error) {
	path, _, err := secretsPath(s, flake)
	if err != nil {
		return nil, err
	}
	if path == "-" && c.out == "" {
		return nil, errors.New("the secrets file from standard input cannot be updated in place; pass --out")
	}
	var lock *os.File
	// The lock is taken before the file is read, also with --out: a second command would read a
	// rotation's state while the first one moves it on, and drive the nodes from it.
	if path != "-" {
		if lock, err = lockSecrets(path); err != nil {
			return nil, err
		}
	}
	f, err := a.readSecretsFile(ctx, s, path, c)
	if err != nil {
		if lock != nil {
			lock.Close()
		}
		return nil, err
	}
	f.lock = lock
	return f, nil
}

// lockSecrets takes an exclusive lock on <file>.lock beside the file a link points to, held
// until the returned file is closed or the process ends. The lock file is never removed: a
// command that removed it could leave the next two to lock different files.
func lockSecrets(path string) (*os.File, error) {
	name := path
	if real, err := filepath.EvalSymlinks(path); err == nil {
		name = real
	}
	name += ".lock"
	lock, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock the secrets file: %w", err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("another chalkctl command is changing %s; wait for it to end, then run this one again", path)
		}
		return nil, fmt.Errorf("lock the secrets file with %s: %w", name, err)
	}
	return lock, nil
}

// readSecretsFile is openSecrets once the file is locked.
func (a *app) readSecretsFile(ctx context.Context, s secretFlags, path string, c changeFlags) (*secretsFile, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(a.stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("read the secrets file: %w", err)
	}
	format, err := pki.DetectFormat(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	secrets, err := pki.ReadSecrets(data, func() ([]age.Identity, error) { return a.ageIdentities(ctx, s.identities) })
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	f := &secretsFile{secrets: secrets, path: path, out: path, format: format, recipients: secrets.Recipients, sum: sha256.Sum256(data)}
	public := ""
	if path != "-" {
		public = filepath.Join(filepath.Dir(path), "secrets.pub.json")
	}
	switch {
	case c.publicOut != "":
		f.publicOut, f.publicNew = c.publicOut, true
		if err := mustNotExist(c.publicOut); err != nil {
			return nil, err
		}
	case c.out == "":
		f.publicOut = public
		if err := matchesPublic(public, secrets); err != nil {
			return nil, err
		}
	}
	if c.out != "" {
		f.out, f.outNew = c.out, true
		if err := mustNotExist(c.out); err != nil {
			return nil, err
		}
	}
	toAge := strings.HasSuffix(c.out, ".age")
	if format == pki.FormatJSON {
		switch {
		case len(c.recipients) > 0 && !toAge:
			return nil, fmt.Errorf("%s is plaintext; --recipient encrypts it only to a new .age file given with --out", path)
		case len(c.recipients) == 0 && toAge:
			return nil, fmt.Errorf("%s is plaintext; pass --recipient to encrypt it to %s", path, c.out)
		case toAge:
			f.format = pki.FormatAge
			fmt.Fprintf(a.stdout, "%s is plaintext; %s is age-encrypted to %s\n", path, c.out, strings.Join(c.recipients, ", "))
		}
	}
	if len(c.recipients) > 0 {
		f.recipients = c.recipients
	}
	if f.format != pki.FormatJSON && len(f.recipients) == 0 {
		return nil, fmt.Errorf("%s is encrypted and records no recipients to encrypt it to again; pass --recipient", path)
	}
	// A recipient that does not parse fails now, before anything changed.
	if _, err := a.ageRecipients(f.recipients); err != nil {
		return nil, err
	}
	return f, nil
}

// matchesPublic refuses to update a secrets.pub.json in place that belongs to other secrets: its
// OS CA must be the secrets' or one they accept, as after a rotation's switch written with --out.
// None is fine; it is created.
func matchesPublic(path string, secrets pki.Secrets) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	public, err := pki.ReadPublic(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	have, err := pki.Fingerprints(public.OSCA.Certificate)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	known, err := pki.Fingerprints(secrets.OSCABundle())
	if err != nil {
		return err
	}
	for _, fp := range have {
		if !slices.Contains(known, fp) {
			return fmt.Errorf("%s holds an OS CA the secrets file neither holds nor accepts; it belongs to other secrets. Write the public half to a new file with --public-out", path)
		}
	}
	return nil
}

// written says where the changes went and what the operator does with them.
func (f *secretsFile) written() string {
	var what []string
	if f.inPlace() {
		what = append(what, "the previous version is "+previousOf(f.path))
	} else {
		what = append(what, "replace the secrets file with it")
	}
	if f.publicOut == "" {
		what = append(what, "secrets.pub.json still holds the previous public half (--public-out writes the new one)")
	} else {
		what = append(what, f.publicOut+" holds the new public half")
	}
	return strings.Join(what, "; ")
}

// previousOf is where replaceFile keeps the previous version of path: beside the file a link
// points to.
func previousOf(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	return path + ".prev"
}

// mustNotExist refuses a file a command was asked to create.
func mustNotExist(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s exists; --out and --public-out write new files", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (a *app) ageRecipients(recipients []string) ([]age.Recipient, error) {
	var parsed []age.Recipient
	for _, r := range recipients {
		rec, err := pki.ParseRecipient(r, a.pluginUI())
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, rec)
	}
	return parsed, nil
}

// writeSecretsFile writes the secrets, and their public half, where openSecrets decided. An
// encrypted file records its recipients inside and names them.
func (a *app) writeSecretsFile(f *secretsFile, secrets pki.Secrets) error {
	if f.format != pki.FormatJSON {
		secrets.Recipients = f.recipients
	}
	if err := secrets.Validate(); err != nil {
		return err
	}
	recipients, err := a.ageRecipients(f.recipients)
	if err != nil {
		return err
	}
	data, err := secrets.EncodeAs(f.format, recipients...)
	if err != nil {
		return err
	}
	if f.inPlace() {
		if err := f.unchanged(); err != nil {
			return err
		}
	}
	if err := a.writeOrReplace(f.out, data, 0o600, &f.outNew); err != nil {
		return err
	}
	if f.inPlace() {
		f.sum = sha256.Sum256(data)
	}
	if f.format != pki.FormatJSON {
		a.sayRecipients(f.out, f.recipients)
	}
	f.secrets = secrets
	if f.publicOut == "" {
		return nil
	}
	public, err := encodePublic(secrets)
	if err != nil {
		return err
	}
	return a.writeOrReplace(f.publicOut, public, 0o644, &f.publicNew)
}

// unchanged refuses to replace a secrets file that changed since this command read or wrote it:
// the lock keeps other chalkctl commands out, but not an editor or a checkout, whose change would
// be lost.
func (f *secretsFile) unchanged() error {
	data, err := os.ReadFile(f.path)
	if err != nil {
		return fmt.Errorf("read the secrets file again before replacing it: %w", err)
	}
	if sha256.Sum256(data) != f.sum {
		return fmt.Errorf("%s changed since chalkctl read it and was left as it is; look at what changed it, then run the command again", f.path)
	}
	return nil
}

// writeOrReplace creates a file while *create is set, and clears it, or replaces the file in
// place keeping its previous version. Either way the directory is synced afterwards; once the
// file is in place, a failed sync is a warning: the command did what it was asked, and the next
// write retries it.
func (a *app) writeOrReplace(path string, data []byte, perm fs.FileMode, create *bool) error {
	var err error
	if *create {
		if err = writeNew(path, data, perm); err != nil {
			return err
		}
		*create = false
		if serr := syncDir(filepath.Dir(path)); serr != nil {
			err = &unsyncedError{path, serr}
		}
	} else {
		err = replaceFile(path, data, perm)
	}
	var unsynced *unsyncedError
	if errors.As(err, &unsynced) {
		fmt.Fprintf(a.stderr, "chalkctl: warning: %v\n", err)
		return nil
	}
	return err
}

// unsyncedError is a file written in place whose directory could not be synced, so a crash may
// still bring back what the directory held before.
type unsyncedError struct {
	path string
	err  error
}

func (e *unsyncedError) Error() string {
	return fmt.Sprintf("%s is written, but its directory was not synced, so a crash may undo that: %v", e.path, e.err)
}

func (e *unsyncedError) Unwrap() error { return e.err }

// replaceFile replaces path with data in one step and keeps what it held as path.prev. The data
// goes to a temporary file beside path and is synced; path.prev becomes a link to the current
// file, and the temporary file is renamed over path. path exists throughout, and a crash leaves
// one version or the other, never part of one. A file that does not exist yet is created with
// perm; an existing one keeps its mode. A symbolic link is followed: the file it points to is
// replaced and the link kept. A directory that cannot be synced after the rename is an
// *unsyncedError.
func replaceFile(path string, data []byte, perm fs.FileMode) error {
	switch real, err := filepath.EvalSymlinks(path); {
	case err == nil:
		path = real
	case !errors.Is(err, fs.ErrNotExist):
		return err
	default:
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%s is a link to a file that does not exist", path)
		}
	}
	info, err := os.Stat(path)
	exists := err == nil
	switch {
	case exists:
		perm = info.Mode().Perm()
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = tmp.Chmod(perm)
	if err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if exists {
		if err := keepPrevious(path, perm); err != nil {
			return fmt.Errorf("keep the previous version of %s: %w", path, err)
		}
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return &unsyncedError{path, err}
	}
	return nil
}

// keepPrevious makes path.prev what path holds now, replacing an older path.prev in one step: a
// hard link where the file system has them, a synced copy otherwise.
func keepPrevious(path string, perm fs.FileMode) error {
	next := path + ".prev.new"
	os.Remove(next)
	if err := os.Link(path, next); err != nil {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if err := createNew(next, perm, func(w io.Writer) error {
			_, err := w.Write(data)
			return err
		}); err != nil {
			return err
		}
	}
	return os.Rename(next, path+".prev")
}

// syncDir syncs a directory, so the names of the files written into it last; tests replace it.
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
