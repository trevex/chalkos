package chalkctl

import (
	"context"
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
	"golang.org/x/term"

	"github.com/trevex/chalkos/pkg/atomicfile"
	"github.com/trevex/chalkos/pkg/clustersecrets"
	"github.com/trevex/chalkos/pkg/internal/cli"
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
	fs.StringVar(&s.path, "secrets", "", "secrets file, encrypted with age or not, or - to read it from standard input")
	fs.Var(&s.identities, "identity", "file holding an age identity to decrypt the secrets with; may be repeated (default ~/.config/chalkos/age.key, ~/.ssh/id_ed25519, ~/.ssh/id_rsa)")
}

// loadSecrets reads the secrets file in whatever format it has. Age identities are read only when
// the file is encrypted.
func (a *app) loadSecrets(ctx context.Context, s secretFlags) (clustersecrets.Secrets, error) {
	path, err := secretsPath(s)
	if err != nil {
		return clustersecrets.Secrets{}, err
	}
	var data []byte
	if path == "-" {
		data, err = io.ReadAll(a.stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return clustersecrets.Secrets{}, fmt.Errorf("read the secrets file: %w", err)
	}
	secrets, err := clustersecrets.Read(data, func() ([]age.Identity, error) { return a.ageIdentities(ctx, s.identities) })
	if err != nil {
		return clustersecrets.Secrets{}, fmt.Errorf("%s: %w", path, err)
	}
	return secrets, nil
}

// secretsPath is the secrets file --secrets names, - for standard input. chalkctl looks for no
// secrets file of its own accord: the file holds every key of the cluster, so it is read only
// where it is named.
func secretsPath(s secretFlags) (string, error) {
	if s.path == "" {
		return "", errors.New("pass the secrets file with --secrets FILE, or --secrets - to read it from standard input")
	}
	return s.path, nil
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
	ids, err := clustersecrets.ParseIdentities(data, a.pluginUI(), a.passphrase(ctx, file), func() ([]byte, error) {
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
func (a *app) passphrase(ctx context.Context, file string) clustersecrets.SSHPassphrase {
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

type recoveryKeyFlags struct {
	cluster clusterFlags
	secrets secretFlags
}

func (a *app) recoveryKeyCommand() *cobra.Command {
	var f recoveryKeyFlags
	cmd := a.command(&cobra.Command{
		Use:     "recovery-key <node>",
		Short:   "Print a node's recovery key",
		Long:    recoveryKeyLong,
		Example: recoveryKeyExample,
		Args:    cli.ExactArgs(1),
	}, func(a *app, ctx context.Context, pos []string) error { return a.recoveryKey(ctx, f, pos) })
	f.cluster.register(cmd.Flags())
	f.secrets.register(cmd.Flags())
	return cmd
}

func (a *app) recoveryKey(ctx context.Context, f recoveryKeyFlags, pos []string) error {
	c, err := loadCluster(ctx, f.cluster)
	if err != nil {
		return err
	}
	if err := c.requireNodes("recovery-key"); err != nil {
		return err
	}
	if err := c.node(pos[0]); err != nil {
		return err
	}
	secrets, err := a.loadSecrets(ctx, f.secrets)
	if err != nil {
		return err
	}
	key, err := clustersecrets.RecoveryKey(secrets.RecoverySecret, pos[0])
	if err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, key)
	return nil
}

type genSecretsFlags struct {
	recipients     stringList
	plaintext      bool
	out, publicOut string
}

func (a *app) genSecretsCommand() *cobra.Command {
	var f genSecretsFlags
	cmd := a.command(&cobra.Command{
		Use:     "secrets",
		Short:   "Generate the cluster's secrets file",
		Long:    genSecretsLong,
		Example: genSecretsExample,
		Args:    cli.NoArgs,
	}, func(a *app, ctx context.Context, pos []string) error { return a.genSecrets(f) })
	fs := cmd.Flags()
	fs.Var(&f.recipients, "recipient", "age recipient to encrypt secrets.age to: an age public key, an SSH public key, or an age plugin recipient; may be repeated")
	fs.BoolVar(&f.plaintext, "plaintext", false, "write secrets.json unencrypted, for files protected by other means")
	fs.StringVar(&f.out, "out", ".", "directory to write the files to, or - to write the secrets file alone to standard output")
	fs.StringVar(&f.publicOut, "public-out", "", "new file to write the public half to (default secrets.pub.json in --out; required with --out -)")
	return cmd
}

func (a *app) genSecrets(f genSecretsFlags) error {
	recipients := f.recipients
	if (len(recipients) == 0) == !f.plaintext {
		return cli.Usagef("pass --recipient (one or more) or --plaintext")
	}
	if f.out == "-" {
		if f.publicOut == "" {
			return cli.Usagef("gen secrets --out - needs --public-out, the file the public half goes to, which images trust the OS CA from")
		}
		if err := publicDirExists(f.publicOut); err != nil {
			return err
		}
		return a.genSecretsToStdout(recipients, f.plaintext, f.publicOut)
	}
	name := "secrets.age"
	if f.plaintext {
		name = "secrets.json"
	}
	secretsFile := filepath.Join(f.out, name)
	publicPath := f.publicOut
	if publicPath == "" {
		publicPath = filepath.Join(f.out, "secrets.pub.json")
	}
	// The secrets file is written once: a new one would orphan every installed node.
	for _, p := range []string{filepath.Join(f.out, "secrets.age"), filepath.Join(f.out, "secrets.json"), publicPath} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s exists; the secrets are generated once per cluster", p)
		}
	}
	if err := publicDirExists(publicPath); err != nil {
		return err
	}

	secrets, err := clustersecrets.Generate(time.Now())
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

// publicDirExists refuses a --public-out whose directory does not exist before any secret is
// generated, so the secrets never go out without their public half.
func publicDirExists(path string) error {
	dir := filepath.Dir(path)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("the directory %s of %s does not exist", dir, path)
	}
	return nil
}

// genSecretsToStdout writes new secrets to standard output, for a secret manager to keep, their
// public half to a new file, and what it says to standard error. The public half is written
// first, and removed again when the secrets cannot be written, so it never belongs to secrets
// nobody kept.
func (a *app) genSecretsToStdout(recipients []string, plaintext bool, publicPath string) error {
	secrets, err := clustersecrets.Generate(time.Now())
	if err != nil {
		return err
	}
	data, err := a.encodeSecrets(secrets, recipients, plaintext)
	if err != nil {
		return err
	}
	public, err := encodePublic(secrets)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteNew(publicPath, public, 0o644); err != nil {
		return err
	}
	if _, err := a.stdout.Write(data); err != nil {
		os.Remove(publicPath)
		return err
	}
	if plaintext {
		fmt.Fprintln(a.stderr, "warning: the secrets went to standard output unencrypted; protect them by other means")
	} else {
		fmt.Fprintf(a.stderr, "encrypted the secrets to %s\n", strings.Join(recipients, ", "))
	}
	fmt.Fprintf(a.stderr, "wrote %s\n", publicPath)
	return nil
}

// writeSecrets writes the secrets file, encrypted to the recipients, which it records inside it,
// unless plaintext, and its public half when publicPath is set. Neither may exist: a secrets file
// is never overwritten.
func (a *app) writeSecrets(secrets clustersecrets.Secrets, recipients []string, plaintext bool, path, publicPath string) error {
	for _, p := range []string{path, publicPath} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s exists; secrets are always written to a new file", p)
		}
	}
	data, err := a.encodeSecrets(secrets, recipients, plaintext)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteNew(path, data, 0o600); err != nil {
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
	return atomicfile.WriteNew(publicPath, public, 0o644)
}

// encodeSecrets encodes the secrets as a secrets file: encrypted to the recipients, which it
// records inside it, unless plaintext.
func (a *app) encodeSecrets(secrets clustersecrets.Secrets, recipients []string, plaintext bool) ([]byte, error) {
	if plaintext {
		return secrets.Encode()
	}
	parsed, err := a.ageRecipients(recipients)
	if err != nil {
		return nil, err
	}
	secrets.Recipients = recipients
	return secrets.Encrypt(parsed...)
}

// sayRecipients names whom an encrypted secrets file was encrypted to, so an operator notices a
// recipient that does not belong.
func (a *app) sayRecipients(path string, recipients []string) {
	fmt.Fprintf(a.stdout, "encrypted %s to %s\n", path, strings.Join(recipients, ", "))
}

// encodePublic encodes the public half of the secrets.
func encodePublic(secrets clustersecrets.Secrets) ([]byte, error) {
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
	fs.StringVar(&c.out, "out", "", "new file to write the changed secrets file to, instead of updating the secrets file in place")
	fs.StringVar(&c.publicOut, "public-out", "", "new file to write the public half to, instead of updating secrets.pub.json beside the secrets file in place")
	fs.Var(&c.recipients, "recipient", "age recipient to encrypt the changed secrets file to instead of those it records; may be repeated. A plaintext secrets file is encrypted only to a new .age file given with --out")
}

// secretsFile is a secrets file a command changes, with where and how the changes are written:
// back to the file in place, keeping its previous version as <file>.prev, the one from before a
// rotation while one runs, or to new files; in the format the file had, an encrypted file to the
// recipients it records.
type secretsFile struct {
	secrets clustersecrets.Secrets
	// path is the file the secrets were read from, out the one they are written to.
	path, out string
	// publicOut receives the public half; empty leaves it alone.
	publicOut  string
	format     clustersecrets.Format
	recipients []string
	// outNew and publicNew are set while out and publicOut are new files that were not written
	// yet; once written, later writes replace them in place.
	outNew, publicNew bool
	// file holds the secrets file's lock until Close; nil for standard input.
	file *clustersecrets.File
	// duringRotation is set once a write replaced a file that recorded a rotation, which left the
	// file's previous version the one from before the rotation.
	duringRotation bool
}

// Close releases the secrets file's lock.
func (f *secretsFile) Close() error {
	if f.file == nil {
		return nil
	}
	return f.file.Close()
}

// inPlace reports whether the changes go back to the file they were read from.
func (f *secretsFile) inPlace() bool { return f.out == f.path }

// openSecrets reads the secrets file for a command that changes it, and works out where and how
// the changes are written before anything is changed. The recipients come from inside the
// encrypted file, or from --recipient, never from secrets.pub.json, which anyone who can write
// beside the secrets file could change.
func (a *app) openSecrets(ctx context.Context, s secretFlags, change changeFlags) (*secretsFile, error) {
	path, err := secretsPath(s)
	if err != nil {
		return nil, err
	}
	if path == "-" && change.out == "" {
		return nil, errors.New("the secrets file from standard input cannot be updated in place; pass --out")
	}
	if path == "-" {
		data, err := io.ReadAll(a.stdin)
		if err != nil {
			return nil, fmt.Errorf("read the secrets file: %w", err)
		}
		return a.readSecretsFile(ctx, s, path, data, change)
	}
	// The file is locked also with --out: a second command would read a rotation's state while
	// the first one moves it on, and drive the nodes from it.
	file, data, err := clustersecrets.Open(path)
	if errors.Is(err, clustersecrets.ErrLocked) {
		return nil, fmt.Errorf("another chalkctl command is changing %s; wait for it to end, then run this one again", path)
	}
	if err != nil {
		return nil, err
	}
	f, err := a.readSecretsFile(ctx, s, path, data, change)
	if err != nil {
		file.Close()
		return nil, err
	}
	f.file = file
	return f, nil
}

// readSecretsFile is openSecrets once the file is read.
func (a *app) readSecretsFile(ctx context.Context, s secretFlags, path string, data []byte, c changeFlags) (*secretsFile, error) {
	format, err := clustersecrets.DetectFormat(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	secrets, err := clustersecrets.Read(data, func() ([]age.Identity, error) { return a.ageIdentities(ctx, s.identities) })
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	f := &secretsFile{secrets: secrets, path: path, out: path, format: format, recipients: secrets.Recipients}
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
	if format == clustersecrets.FormatJSON {
		switch {
		case len(c.recipients) > 0 && !toAge:
			return nil, fmt.Errorf("%s is plaintext; --recipient encrypts it only to a new .age file given with --out", path)
		case len(c.recipients) == 0 && toAge:
			return nil, fmt.Errorf("%s is plaintext; pass --recipient to encrypt it to %s", path, c.out)
		case toAge:
			f.format = clustersecrets.FormatAge
			fmt.Fprintf(a.stdout, "%s is plaintext; %s is age-encrypted to %s\n", path, c.out, strings.Join(c.recipients, ", "))
		}
	}
	if len(c.recipients) > 0 {
		f.recipients = c.recipients
	}
	if f.format != clustersecrets.FormatJSON && len(f.recipients) == 0 {
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
func matchesPublic(path string, secrets clustersecrets.Secrets) error {
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
	public, err := clustersecrets.ReadPublic(data)
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
		what = append(what, "the previous version is "+clustersecrets.PreviousOf(f.path))
		if f.duringRotation {
			what[len(what)-1] = "the version from before the rotation is " + clustersecrets.PreviousOf(f.path)
		}
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
		rec, err := clustersecrets.ParseRecipient(r, a.pluginUI())
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, rec)
	}
	return parsed, nil
}

// writeSecretsFile writes the secrets, and their public half, where openSecrets decided. An
// encrypted file records its recipients inside and names them.
func (a *app) writeSecretsFile(f *secretsFile, secrets clustersecrets.Secrets) error {
	if f.format != clustersecrets.FormatJSON {
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
	// While the file records a rotation, its previous version stays the one from before the
	// rotation: each phase writes the file, and the version to go back to is the one the rotation
	// started from.
	rotating := f.secrets.Rotation != nil
	switch {
	case f.inPlace() && rotating:
		err = f.file.Overwrite(data)
	case f.inPlace():
		err = f.file.Replace(data)
	case rotating && !f.outNew:
		err = clustersecrets.Overwrite(f.out, data, 0o600)
	default:
		err = a.writeOrReplace(f.out, data, 0o600, &f.outNew)
	}
	if errors.Is(err, clustersecrets.ErrChanged) {
		return fmt.Errorf("%s changed since chalkctl read it and was left as it is; look at what changed it, then run the command again", f.path)
	}
	if err := a.warnUnsynced(err); err != nil {
		return err
	}
	f.duringRotation = rotating
	if f.format != clustersecrets.FormatJSON {
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
	return a.warnUnsynced(a.writeOrReplace(f.publicOut, public, 0o644, &f.publicNew))
}

// writeOrReplace creates a file while *create is set, and clears it, or replaces the file in
// place keeping its previous version. Either way the directory is synced afterwards.
func (a *app) writeOrReplace(path string, data []byte, perm fs.FileMode, create *bool) error {
	if !*create {
		return clustersecrets.Replace(path, data, perm)
	}
	err := clustersecrets.Create(path, data, perm)
	var unsynced *clustersecrets.UnsyncedError
	if err == nil || errors.As(err, &unsynced) {
		*create = false
	}
	return err
}

// warnUnsynced turns a file written in place whose directory could not be synced into a warning:
// the command did what it was asked, and the next write retries the sync.
func (a *app) warnUnsynced(err error) error {
	var unsynced *clustersecrets.UnsyncedError
	if errors.As(err, &unsynced) {
		fmt.Fprintf(a.stderr, "chalkctl: warning: %v\n", err)
		return nil
	}
	return err
}
