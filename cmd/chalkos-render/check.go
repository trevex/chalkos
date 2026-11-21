package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// The checks of outputs: programs that read the files a renderer wrote into $OUT and fail to
// refuse them. chalkd runs a check without a shell, so these run a service's own check of its
// config with the paths of the files in its arguments, or check what the service's check passes
// but the renderer never writes.

// check runs the check command line args on the files in out.
func check(args []string, out string) error {
	if len(args) == 0 {
		return errUsage
	}
	switch {
	case args[0] == "run" && len(args) > 1:
		return runCheck(expand(args[1:], out, ""))
	case args[0] == "each" && len(args) > 2:
		return checkEach(args[1], args[2:], out)
	case args[0] == "chrony" && len(args) == 2:
		return checkChrony(args[1], out)
	case args[0] == "chrony-server" && len(args) == 2:
		return checkChronyServer(args[1], out)
	case args[0] == "kea" && len(args) == 3:
		return checkKea(args[1], args[2], out)
	}
	return errUsage
}

// expand is args with $OUT replaced by out and, for a file, $FILE by file.
func expand(args []string, out, file string) []string {
	pairs := []string{"$OUT", out}
	if file != "" {
		pairs = append(pairs, "$FILE", file)
	}
	r := strings.NewReplacer(pairs...)
	expanded := make([]string, len(args))
	for i, a := range args {
		expanded[i] = r.Replace(a)
	}
	return expanded
}

// maxCheckOutput is how much of what a failed command printed its error keeps.
const maxCheckOutput = 8 << 10

// runCheck runs the command argv without input and fails when it fails, with the start of what it
// printed on standard output and standard error, which goes to the journal alone. A service's
// check prints its errors on either.
func runCheck(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	var printed bytes.Buffer
	cmd.Stdout, cmd.Stderr = &printed, &printed
	err := cmd.Run()
	if err == nil {
		return nil
	}
	text := printed.Bytes()
	if len(text) > maxCheckOutput {
		text = text[:maxCheckOutput]
	}
	return fmt.Errorf("%s refused the files (%w): %s", filepath.Base(argv[0]), err, bytes.TrimSpace(text))
}

// checkEach runs argv once for every file of out that glob matches, in the order of their names,
// with $FILE replaced by the file's path; no file matching passes.
func checkEach(glob string, argv []string, out string) error {
	if !filepath.IsLocal(glob) {
		return fmt.Errorf("%q matches no files below $OUT", glob)
	}
	files, err := filepath.Glob(filepath.Join(out, glob))
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := runCheck(expand(argv, out, f)); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
	}
	return nil
}

// checkLines refuses the file out/name when one of its lines does not match line; a missing file
// passes. what is what a matching line is, for the error.
func checkLines(out, name string, line *regexp.Regexp, what string) error {
	f, err := os.Open(filepath.Join(out, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for n := 1; s.Scan(); n++ {
		if !line.MatchString(s.Text()) {
			return fmt.Errorf("%s:%d is no %s", name, n, what)
		}
	}
	return s.Err()
}

// chronyParse runs chronyd -p, which parses the config without running chrony, on a config of
// the directive alone.
func chronyParse(chronyd, directive string) error {
	f, err := os.CreateTemp("", "chalkos-check-chrony-*.conf")
	if err != nil {
		return err
	}
	conf := f.Name()
	defer os.Remove(conf)
	_, err = f.WriteString(directive + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return runCheck([]string{chronyd, "-p", "-f", conf})
}

// chronySource is a line of servers.sources, the time sources of the common output chrony, as
// its template writes it: chronyd -p takes other directives in a sourcedir file, which chrony
// then ignores.
var chronySource = regexp.MustCompile(`^server [A-Za-z0-9][A-Za-z0-9.:-]* iburst( nts)?( port [0-9]+)?$`)

// checkChrony checks the output chrony: chronyd parses servers.sources, and each of its lines is
// a server of a host.
func checkChrony(chronyd, out string) error {
	if err := chronyParse(chronyd, "include "+filepath.Join(out, "servers.sources")); err != nil {
		return err
	}
	return checkLines(out, "servers.sources", chronySource, "server of a host")
}

// chronyServerLine is a line of the router's server.conf as renderChronyServer writes it:
// chronyd -p takes any subnet of allow, such as 10 or all.
var chronyServerLine = regexp.MustCompile(`^(# .*|allow [0-9A-Fa-f.:]+/[0-9]+|local stratum ([1-9]|1[0-5]) orphan|ratelimit interval 1 burst 16)$`)

// checkChronyServer checks the router's output chrony-server: chronyd parses the files of the
// directory as its confdir, and each line of server.conf is a directive the renderer writes.
func checkChronyServer(chronyd, out string) error {
	if err := chronyParse(chronyd, "confdir "+out); err != nil {
		return err
	}
	return checkLines(out, chronyServerFile, chronyServerLine, "allow, local or ratelimit directive of the renderer")
}
