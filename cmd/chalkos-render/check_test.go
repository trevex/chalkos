package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tool writes an executable shell script that stands in for a service's check or for jq, and
// returns its path. It may append to $LOG, which the test reads back with logged.
func tool(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -e\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// withLog points $LOG at a file of the test's and returns a function that reads it.
func withLog(t *testing.T) func() string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("LOG", log)
	return func() string {
		data, _ := os.ReadFile(log)
		return string(data)
	}
}

// checkIn runs the check command line args on the files of out.
func checkIn(out string, args ...string) error {
	return run(append([]string{"check"}, args...), strings.NewReader(""), io.Discard, out)
}

// check run runs the command once with $OUT in its arguments replaced by the output's directory,
// passes when it succeeds and fails with what it printed when it fails.
func TestCheckRun(t *testing.T) {
	logged := withLog(t)
	out := t.TempDir()
	ok := tool(t, `printf '%s\n' "$@" >>"$LOG"`)
	if err := checkIn(out, "run", ok, "--file", "$OUT/ruleset.nft", "--config_dir=$OUT"); err != nil {
		t.Fatal(err)
	}
	if want := "--file\n" + out + "/ruleset.nft\n--config_dir=" + out + "\n"; logged() != want {
		t.Errorf("the command got:\n%s\nwant:\n%s", logged(), want)
	}
	bad := tool(t, `echo "line 3: syntax error" >&2; exit 4`)
	err := checkIn(out, "run", bad, "$OUT/ruleset.nft")
	if err == nil || !strings.Contains(err.Error(), "tool refused the files (exit status 4): line 3: syntax error") {
		t.Errorf("a failing command: %v", err)
	}
}

// check each runs the command once for every file the glob matches, in the order of their
// names, with $FILE replaced by the file; it passes on no match and stops at the first failure,
// which names the file.
func TestCheckEach(t *testing.T) {
	logged := withLog(t)
	out := t.TempDir()
	ok := tool(t, `printf '%s\n' "$*" >>"$LOG"`)
	if err := checkIn(out, "each", "*.conf", ok, "--conf-file=$FILE"); err != nil || logged() != "" {
		t.Fatalf("no file: %v, ran %q", err, logged())
	}
	for _, f := range []string{"b.conf", "a.conf", "c.json"} {
		os.WriteFile(filepath.Join(out, f), nil, 0o644)
	}
	if err := checkIn(out, "each", "*.conf", ok, "--conf-file=$FILE", "$OUT"); err != nil {
		t.Fatal(err)
	}
	if want := "--conf-file=" + out + "/a.conf " + out + "\n--conf-file=" + out + "/b.conf " + out + "\n"; logged() != want {
		t.Errorf("the command ran:\n%s\nwant:\n%s", logged(), want)
	}
	bad := tool(t, `case $1 in */b.conf) echo bad >&2; exit 1;; esac`)
	if err := checkIn(out, "each", "*.conf", bad, "$FILE"); err == nil || !strings.HasPrefix(err.Error(), "b.conf: tool refused the files") {
		t.Errorf("a failing file: %v", err)
	}
	if err := checkIn(out, "each", "../*.conf", ok, "$FILE"); err == nil || !strings.Contains(err.Error(), "below $OUT") {
		t.Errorf("a glob outside $OUT: %v", err)
	}
}

// The chrony checks hand chronyd a config that includes the output's files, then refuse a line
// chronyd takes but the renderer does not write; a missing file passes.
func TestCheckChrony(t *testing.T) {
	logged := withLog(t)
	chronyd := tool(t, `[ "$1 $2" = "-p -f" ]; cat "$3" >>"$LOG"`)
	out := t.TempDir()
	if err := checkIn(out, "chrony", chronyd); err != nil {
		t.Fatalf("no servers: %v", err)
	}
	if want := "include " + out + "/servers.sources\n"; logged() != want {
		t.Errorf("chronyd read:\n%s\nwant:\n%s", logged(), want)
	}
	sources := filepath.Join(out, "servers.sources")
	os.WriteFile(sources, []byte("server a.example iburst nts\nserver 10.0.0.1 iburst port 12300\n"), 0o644)
	if err := checkIn(out, "chrony", chronyd); err != nil {
		t.Errorf("the template's servers: %v", err)
	}
	for _, bad := range []string{"server a.example iburst bogus", "pool a.example iburst", "server a.example iburst nts maxpoll", ""} {
		os.WriteFile(sources, []byte("server a.example iburst\n"+bad+"\n"), 0o644)
		if err := checkIn(out, "chrony", chronyd); err == nil || err.Error() != "servers.sources:2 is no server of a host" {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if err := checkIn(out, "chrony", tool(t, "exit 1")); err == nil {
		t.Error("a config chronyd refuses passes")
	}

	logged = withLog(t)
	if err := checkIn(out, "chrony-server", chronyd); err != nil {
		t.Fatalf("no server.conf: %v", err)
	}
	if want := "confdir " + out + "\n"; logged() != want {
		t.Errorf("chronyd read:\n%s\nwant:\n%s", logged(), want)
	}
	conf := filepath.Join(out, "server.conf")
	os.WriteFile(conf, []byte("# header\nallow 192.0.2.0/24\nallow 2001:db8:1::/64\nratelimit interval 1 burst 16\nlocal stratum 15 orphan\n"), 0o644)
	if err := checkIn(out, "chrony-server", chronyd); err != nil {
		t.Errorf("the renderer's lines: %v", err)
	}
	for _, bad := range []string{"allow 10", "allow all", "local stratum 10", "local stratum 16 orphan", "port 0"} {
		os.WriteFile(conf, []byte(bad+"\n"), 0o644)
		if err := checkIn(out, "chrony-server", chronyd); err == nil || !strings.HasPrefix(err.Error(), "server.conf:1 is no allow") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// The kea check runs each server's program on its config without the links it names, with its
// numbers as they are.
func TestCheckKea(t *testing.T) {
	logged := withLog(t)
	kea := func(family string) string {
		return tool(t, `[ "$1" = -t ]; echo `+family+` >>"$LOG"; cat "$2" >>"$LOG"; echo >>"$LOG"`)
	}
	out := t.TempDir()
	os.WriteFile(filepath.Join(out, "kea-dhcp4.json"), []byte(`{"Dhcp4": {"interfaces-config": {"interfaces": ["lan0"]}, "valid-lifetime": 4294967295, "subnet4": [{"id": 1, "interface": "lan0"}]}}`), 0o644)
	os.WriteFile(filepath.Join(out, "kea-dhcp6-blue.json"), []byte(`{"Dhcp6": {"interfaces-config": {"interfaces": ["lan1"], "re-detect": false}, "subnet6": [{"id": 2, "interface": "lan1"}]}}`), 0o644)
	if err := checkIn(out, "kea", kea("dhcp4"), kea("dhcp6")); err != nil {
		t.Fatal(err)
	}
	want := `dhcp4
{"Dhcp4":{"interfaces-config":{"interfaces":[]},"subnet4":[{"id":1,"interface":"lan0"}],"valid-lifetime":4294967295}}
dhcp6
{"Dhcp6":{"interfaces-config":{"interfaces":[],"re-detect":false},"subnet6":[{"id":2}]}}
`
	if logged() != want {
		t.Errorf("kea read:\n%s\nwant:\n%s", logged(), want)
	}
	if err := checkIn(out, "kea", kea("dhcp4"), tool(t, "echo 'DHCP6_CONFIG_LOAD_FAIL' >&2; exit 1")); err == nil || !strings.HasPrefix(err.Error(), "kea-dhcp6-blue.json: tool refused the files") || !strings.Contains(err.Error(), "DHCP6_CONFIG_LOAD_FAIL") {
		t.Errorf("a config Kea refuses: %v", err)
	}
	os.WriteFile(filepath.Join(out, "kea-dhcp6-blue.json"), []byte(`{"Dhcp4": {}}`), 0o644)
	if err := checkIn(out, "kea", kea("dhcp4"), kea("dhcp6")); err == nil || err.Error() != "kea-dhcp6-blue.json: the config holds no Dhcp6 server" {
		t.Errorf("a config of another family: %v", err)
	}
}

// The jq renderer writes what jq prints, raw, into the file it names; the jq validator prints it
// compact as the report, and takes the validator's arguments chalkd adds.
func TestJQ(t *testing.T) {
	jq := tool(t, `printf '%s %s %s\n' "$1" "$2" "$3"; cat`)
	out, err := render(t, []string{"jq", jq, "/nix/store/x-bird.conf.jq", "bird.conf"}, peers)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(out, "bird.conf"))
	if want := "--raw-output --from-file /nix/store/x-bird.conf.jq\n" + peers; string(got) != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", got, want)
	}
	if _, err := render(t, []string{"jq", jq, "x.jq", "../bird.conf"}, peers); err == nil || !strings.Contains(err.Error(), "below $OUT") {
		t.Errorf("a file outside $OUT: %v", err)
	}
	if _, err := render(t, []string{"jq", tool(t, "exit 5"), "x.jq", "bird.conf"}, peers); err == nil || !strings.Contains(err.Error(), "exit status 5") {
		t.Errorf("a failing program: %v", err)
	}

	var report bytes.Buffer
	if err := run([]string{"validate-jq", jq, "peers.jq", "validate", "bgp-peers"}, strings.NewReader("{}"), &report, ""); err != nil {
		t.Fatal(err)
	}
	if want := "--compact-output --from-file peers.jq\n{}"; report.String() != want {
		t.Errorf("reported %q, want %q", report.String(), want)
	}
}

func TestCheckUsage(t *testing.T) {
	out := t.TempDir()
	for _, args := range [][]string{{}, {"run"}, {"each", "*.conf"}, {"chrony"}, {"kea", "x"}, {"other", "x"}} {
		if err := checkIn(out, args...); !errors.Is(err, errUsage) {
			t.Errorf("check %v: %v", args, err)
		}
	}
	for _, args := range [][]string{{"validate-jq", "jq", "x.jq"}, {"validate-jq", "jq", "x.jq", "other", "x"}, {"jq", "jq", "x.jq"}} {
		if _, err := render(t, args, peers); !errors.Is(err, errUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
	if err := run([]string{"check", "run", "true"}, strings.NewReader(""), io.Discard, ""); err == nil || !strings.Contains(err.Error(), "$OUT") {
		t.Errorf("a check without $OUT: %v", err)
	}
}
