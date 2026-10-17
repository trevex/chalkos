// Package facts loads the node state change rules read as facts.<name>: the JSON content of a
// file, or the JSON a program of the image's store prints, run in a sandbox. The image declares
// its facts in /etc/chalkos/facts.json.
package facts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"strings"
	"time"
)

const (
	// DefaultPath is where an image declares its facts.
	DefaultPath = "/etc/chalkos/facts.json"
	// FallbackSecret is the input that gives a command the fallback secret delivered with the
	// config, the only input there is.
	FallbackSecret = "fallbackSecret"
	// Timeout and MaxOutput bound a command: a delivery waits for its facts under the config lock.
	Timeout   = 30 * time.Second
	MaxOutput = 1 << 20
)

// Definition is a fact as facts.json declares it: a file, with the fields of it the fact keeps, or
// a command with the inputs it takes.
type Definition struct {
	File string `json:"file,omitempty"`
	// Fields are the keys of the file's object the fact keeps, so a rule and its messages see
	// nothing else of the file, such as the private keys beside a share's kind; none keeps it all.
	Fields  []string `json:"fields,omitempty"`
	Command string   `json:"command,omitempty"`
	Inputs  []string `json:"inputs,omitempty"`
}

// Read reads the facts an image declares; a missing file declares none.
func Read(path string) (map[string]Definition, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]Definition{}, nil
	}
	if err != nil {
		return nil, err
	}
	var defs map[string]Definition
	if err := json.Unmarshal(data, &defs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return defs, nil
}

// Runner runs a command fact's program with stdin and returns what it prints, at most limit
// bytes; more is an error.
type Runner interface {
	Run(ctx context.Context, argv []string, stdin []byte, limit int) ([]byte, error)
}

// Loader loads the facts of one delivery, each once, when a rule first reads it. A loader For
// another input shares what it loaded: a file is read once a delivery, and a command runs once
// for each input it reads.
type Loader struct {
	Definitions map[string]Definition
	Run         Runner
	// Input is what every command reads on standard input, config and oldConfig as rules see
	// them; Inputs holds the inputs a command receives besides when it declares them.
	Input  map[string]any
	Inputs map[string]any
	// Install reads every fact as null, files and commands alike, as at an install: the node has
	// no state yet for its facts to read, and what an earlier attempt that failed left behind, such
	// as a storage.json, is not the node's.
	Install bool
	// Timeout bounds each command; zero is Timeout.
	Timeout time.Duration

	ctx context.Context
	// loaded holds the facts by name as this loader's rules read them, overrides among them;
	// shared the files and the commands' results of the delivery, by fact and input.
	loaded map[string]loaded
	shared map[string]loaded
}

type loaded struct {
	value any
	err   error
}

// NewLoader is a loader for one delivery whose commands run under ctx.
func NewLoader(ctx context.Context, defs map[string]Definition, run Runner) *Loader {
	return &Loader{Definitions: defs, Run: run, ctx: ctx, loaded: map[string]loaded{}, shared: map[string]loaded{}}
}

// For is a loader of the same delivery whose commands read input instead, as for a check of
// another config set than the delivered one. It shares the facts l loaded, but not its overrides.
func (l *Loader) For(input map[string]any) *Loader {
	other := *l
	other.Input, other.loaded = input, map[string]loaded{}
	return &other
}

// Override makes name read as v for this loader, whatever the image declares, as for a check of
// a config against storage that is yet to be made.
func (l *Loader) Override(name string, v any) {
	l.loaded[name] = loaded{value: v}
}

// Fact is the value of the fact name: null for a fact the image does not declare, as a fact a
// newer image's rules read and the running image lacks.
func (l *Loader) Fact(name string) (any, error) {
	if f, ok := l.loaded[name]; ok {
		return f.value, f.err
	}
	v, err := l.load(name)
	l.loaded[name] = loaded{value: v, err: err}
	return v, err
}

func (l *Loader) load(name string) (any, error) {
	def, ok := l.Definitions[name]
	if !ok || l.Install {
		return nil, nil
	}
	key := "file " + name
	var stdin []byte
	if def.File == "" {
		var err error
		if stdin, err = l.stdin(def); err != nil {
			return nil, err
		}
		key = "command " + name + "\x00" + string(stdin)
	}
	if f, ok := l.shared[key]; ok {
		return f.value, f.err
	}
	var v any
	var err error
	if def.File != "" {
		v, err = readFile(def.File, def.Fields)
	} else {
		v, err = l.command(name, def, stdin)
	}
	l.shared[key] = loaded{value: v, err: err}
	return v, err
}

// readFile is a file's JSON, null when it is missing, with only fields kept of its object when
// fields are given.
func readFile(path string, fields []string) (any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v, err := decode(data)
	if err != nil {
		return nil, fmt.Errorf("%s is not JSON", path)
	}
	if len(fields) == 0 {
		return v, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s holds no JSON object to keep fields of", path)
	}
	kept := map[string]any{}
	for _, f := range fields {
		if fv, ok := obj[f]; ok {
			kept[f] = fv
		}
	}
	return kept, nil
}

// stdin is what a command fact reads: the config, and the inputs it declares.
func (l *Loader) stdin(def Definition) ([]byte, error) {
	input := map[string]any{}
	for k, v := range l.Input {
		input[k] = v
	}
	for _, name := range def.Inputs {
		if v, ok := l.Inputs[name]; ok {
			input[name] = v
		}
	}
	return json.Marshal(input)
}

// command runs the command fact name with stdin. What a program that fails wrote to standard error
// and why what it printed is no JSON go to the journal, not into the error, since the program can
// echo the config it reads.
func (l *Loader) command(name string, def Definition, stdin []byte) (any, error) {
	argv := strings.Fields(def.Command)
	if len(argv) == 0 {
		return nil, errors.New("the fact names no command")
	}
	timeout := l.Timeout
	if timeout == 0 {
		timeout = Timeout
	}
	parent := l.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	out, err := l.Run.Run(ctx, argv, stdin, MaxOutput)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return nil, fmt.Errorf("%s did not finish within %s", argv[0], timeout)
	case err != nil:
		var failed *CommandError
		if errors.As(err, &failed) {
			log.Printf("fact %s: %s failed (%s): %q", name, argv[0], failed.Status, failed.Stderr)
		}
		return nil, err
	case len(out) > MaxOutput:
		return nil, fmt.Errorf("%s printed more than %d bytes", argv[0], MaxOutput)
	}
	v, err := decode(out)
	if err != nil {
		log.Printf("fact %s: %s printed no JSON: %v", name, argv[0], err)
		return nil, fmt.Errorf("%s printed no JSON; see the node's journal", argv[0])
	}
	return v, nil
}

// decode reads JSON with its numbers exact, as documents hold them.
func decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more than one JSON value")
	}
	return v, nil
}
