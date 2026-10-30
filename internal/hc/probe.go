package hc

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/djalmajr/soho/internal/jsonjs"
	"github.com/djalmajr/soho/internal/platform"
)

// versionRE matches the `hc --version` stdout line, `hc X.Y.Z`: three
// numeric components, optionally followed by a pre-release or build suffix,
// which the comparison ignores. Anything else stops the probe before
// `server status`, which may start a core.
var versionRE = regexp.MustCompile(`^hc\s+([0-9]+\.[0-9]+\.[0-9]+)(?:[-+][0-9A-Za-z.-]+)?\s*$`)

// HelloLine is the JSON-RPC hello request sent through `hc connect` asking
// for protocol generation 1. The line is written to hc's stdin terminated
// with a newline, the JSON-RPC line framing.
const HelloLine = `{"jsonrpc":"2.0","id":1,"method":"hello","params":{"generation":1}}`

// Capability is what Probe established: the hc version, whether it reaches
// MinVersion, the running core's name, and the generation, methods and
// features of the hello answer. Err is the step that failed, if any: the
// ErrNotInstalled sentinel or an *Error (a decoded CLI error, or a parse
// failure with Kind "" and the failure as the message); an older version
// stops the probe after the first step with Err nil.
type Capability struct {
	Version    string
	VersionOK  bool
	CoreName   string
	Generation int
	Methods    []string
	Features   []string
	Err        error
}

// Probe runs the three capability steps in order and stops at the first
// failure: `hc --version` (parsed as `hc X.Y.Z`), `hc server status
// --json`, and the generation-1 hello through `hc connect`. A version older
// than MinVersion stops the probe after the first step.
func Probe(ctx context.Context, env platform.Env) Capability {
	cap := Capability{}
	version, err := Version(ctx, env)
	if err != nil {
		cap.Err = probeErr(err)
		return cap
	}
	cap.Version = version
	cap.VersionOK = versionAtLeast(version, MinVersion)
	if !cap.VersionOK {
		return cap
	}
	stdout, err := Run(ctx, env, "server", "status")
	if err != nil {
		cap.Err = probeErr(err)
		return cap
	}
	coreName, err := serverStatusCoreName(stdout)
	if err != nil {
		cap.Err = probeErr(err)
		return cap
	}
	cap.CoreName = coreName
	hello, err := Hello(ctx, env)
	if err != nil {
		cap.Err = probeErr(err)
		return cap
	}
	cap.Generation = hello.Generation
	cap.Methods = hello.Methods
	cap.Features = hello.Features
	return cap
}

// probeErr keeps the Probe.Err contract: the ErrNotInstalled sentinel and
// the decoded *Error errors pass through; every other step failure (a
// parse failure) becomes an *Error with Kind "" and the failure as the
// message.
func probeErr(err error) error {
	if err == nil || errors.Is(err, ErrNotInstalled) {
		return err
	}
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	return &Error{Message: err.Error()}
}

// Version runs `hc --version` and parses the `hc X.Y.Z` line. Any other
// output is an error naming the output.
func Version(ctx context.Context, env platform.Env) (string, error) {
	stdout, err := Run(ctx, env, "--version")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(stdout, "\n") {
		if m := versionRE.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("hc --version output is not 'hc X.Y.Z': %q", strings.TrimSpace(stdout))
}

// Hello sends the generation-1 hello through `hc connect --json` and reads
// the one-line JSON-RPC answer. A core of another generation answers the
// unsupported_generation JSON-RPC error; it comes back as an *Error with
// the kind and hint of that answer's data object and its message.
func Hello(ctx context.Context, env platform.Env) (HelloResult, error) {
	stdout, err := RunInput(ctx, env, HelloLine+"\n", "connect")
	if err != nil {
		return HelloResult{}, err
	}
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return parseHello(line)
	}
	return HelloResult{}, fmt.Errorf("hc connect printed no JSON-RPC answer")
}

// HelloResult is the hello answer of a generation-1 core.
type HelloResult struct {
	Generation int
	CoreName   string
	Methods    []string
	Features   []string
	Version    string
}

// parseHello decodes one JSON-RPC hello answer line. An error answer (the
// core speaks another generation) is an *Error; a result answer fills the
// HelloResult.
func parseHello(line string) (HelloResult, error) {
	v, err := jsonjs.Parse([]byte(line))
	if err != nil {
		return HelloResult{}, fmt.Errorf("hc connect answer is not JSON: %q", line)
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return HelloResult{}, fmt.Errorf("hc connect answer is not a JSON object: %q", line)
	}
	if raw, ok := obj.Get("error"); ok {
		errObj, ok := raw.(*jsonjs.Object)
		if !ok {
			return HelloResult{}, fmt.Errorf("hc connect error answer is not an object: %q", line)
		}
		e := &Error{}
		if message, ok := errObj.Get("message"); ok {
			e.Message = jsString(message)
		}
		if data, ok := errObj.Get("data"); ok {
			if dataObj, ok := data.(*jsonjs.Object); ok {
				if kind, ok := dataObj.Get("kind"); ok {
					e.Kind = jsString(kind)
				}
				if hint, ok := dataObj.Get("hint"); ok {
					e.Hint = jsString(hint)
				}
			}
		}
		return HelloResult{}, e
	}
	raw, ok := obj.Get("result")
	if !ok {
		return HelloResult{}, fmt.Errorf("hc connect answer has no result: %q", line)
	}
	res, ok := raw.(*jsonjs.Object)
	if !ok {
		return HelloResult{}, fmt.Errorf("hc connect result is not an object: %q", line)
	}
	hello := HelloResult{}
	if gen, ok := res.Get("generation"); ok {
		if f, ok := gen.(float64); ok {
			hello.Generation = int(f)
		}
	}
	if coreRaw, ok := res.Get("core"); ok {
		if coreObj, ok := coreRaw.(*jsonjs.Object); ok {
			if name, ok := coreObj.Get("name"); ok {
				hello.CoreName = jsString(name)
			}
		}
	}
	hello.Methods = stringSlice(res, "methods")
	hello.Features = stringSlice(res, "features")
	if version, ok := res.Get("version"); ok {
		hello.Version = jsString(version)
	}
	return hello, nil
}

// serverStatusCoreName reads core.name from the `hc server status --json`
// object.
func serverStatusCoreName(stdout string) (string, error) {
	v, err := jsonjs.Parse([]byte(strings.TrimSpace(stdout)))
	if err != nil {
		return "", fmt.Errorf("hc server status output is not JSON: %q", strings.TrimSpace(stdout))
	}
	obj, ok := v.(*jsonjs.Object)
	if !ok {
		return "", fmt.Errorf("hc server status output is not a JSON object: %q", strings.TrimSpace(stdout))
	}
	raw, ok := obj.Get("core")
	if !ok {
		return "", fmt.Errorf("hc server status output has no core object")
	}
	coreObj, ok := raw.(*jsonjs.Object)
	if !ok {
		return "", fmt.Errorf("hc server status core is not an object")
	}
	name, ok := coreObj.Get("name")
	if !ok || jsString(name) == "" {
		return "", fmt.Errorf("hc server status core has no name")
	}
	return jsString(name), nil
}

// versionAtLeast reports whether the dotted version is at least min,
// element by element, missing elements counting as 0.
func versionAtLeast(version, min string) bool {
	a := strings.Split(version, ".")
	b := strings.Split(min, ".")
	for i := 0; i < len(a) || i < len(b); i++ {
		var av, bv int
		if i < len(a) {
			av, _ = strconv.Atoi(a[i])
		}
		if i < len(b) {
			bv, _ = strconv.Atoi(b[i])
		}
		if av != bv {
			return av > bv
		}
	}
	return true
}

// stringSlice reads a JSON array of strings.
func stringSlice(obj *jsonjs.Object, key string) []string {
	raw, ok := obj.Get(key)
	if !ok {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
