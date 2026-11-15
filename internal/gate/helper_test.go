package gate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

const helperFlag = "--gate-helper="

// TestMain doubles as the program the gate runs: the test binary re-executed
// with --gate-helper=<mode> behaves as one small fixture program.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && strings.HasPrefix(os.Args[1], helperFlag) {
		os.Exit(helper(strings.TrimPrefix(os.Args[1], helperFlag), os.Args[2:]))
	}
	os.Exit(m.Run())
}

func helper(mode string, args []string) int {
	switch mode {
	case "echo":
		fmt.Fprint(os.Stdout, "out-line\n")
		fmt.Fprint(os.Stderr, "err-line\n")
		return 0
	case "exit":
		code, _ := strconv.Atoi(args[0])
		return code
	case "sleep":
		// Bounded by itself so a broken test never leaks the process.
		time.Sleep(60 * time.Second)
		return 0
	case "sleep-ms":
		ms, _ := strconv.Atoi(args[0])
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return 0
	case "touch":
		if err := os.WriteFile(args[0], []byte("ran"), 0o600); err != nil {
			return 1
		}
		return 0
	case "big":
		return bigOutput(args)
	case "probe":
		cwd, _ := os.Getwd()
		stdin, _ := io.ReadAll(os.Stdin)
		fmt.Printf("cwd=%s\nprobe=%s\nstdin=%d\n", cwd, os.Getenv("GATE_PROBE"), len(stdin))
		return 0
	case "argv":
		data, _ := json.Marshal(args)
		fmt.Println(string(data))
		return 0
	case "gate":
		return Run(args, os.Stdout, os.Stderr)
	}
	return helperPlatform(mode, args)
}

func bigOutput(args []string) int {
	outBytes, _ := strconv.Atoi(args[0])
	errBytes, _ := strconv.Atoi(args[1])
	write := func(w io.Writer, n int) {
		chunk := make([]byte, 64<<10)
		for written := 0; written < n; {
			size := min(len(chunk), n-written)
			for i := 0; i < size; i++ {
				chunk[i] = patternByte(written + i)
			}
			if _, err := w.Write(chunk[:size]); err != nil {
				os.Exit(3)
			}
			written += size
		}
	}
	write(os.Stdout, outBytes)
	write(os.Stderr, errBytes)
	return 0
}

func patternByte(i int) byte { return byte('a' + i%23) }

func helperArgs(mode string, extra ...string) []string {
	return append([]string{os.Args[0], helperFlag + mode}, extra...)
}

type outcome struct {
	code   int
	out    string
	errOut string
	prefix string
}

func runGate(t *testing.T, prefix, bound string, argv ...string) outcome {
	t.Helper()
	var out, errOut bytes.Buffer
	args := append([]string{prefix, bound, "--"}, argv...)
	code := Run(args, &out, &errOut)
	return outcome{code: code, out: out.String(), errOut: errOut.String(), prefix: prefix}
}

func runGateRaw(t *testing.T, args ...string) outcome {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(args, &out, &errOut)
	return outcome{code: code, out: out.String(), errOut: errOut.String()}
}

func readResult(t *testing.T, prefix string) resultRecord {
	t.Helper()
	data, err := os.ReadFile(prefix + ".result.json")
	if err != nil {
		t.Fatalf("result.json: %v", err)
	}
	var res resultRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&res); err != nil {
		t.Fatalf("result.json %q: %v", data, err)
	}
	return res
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (o outcome) summaryLine(res resultRecord) string {
	return fmt.Sprintf("pid=%d native_exit=%d context_expired=%t wait_joined=%t\n", res.PID, res.NativeExit, res.ContextExpired, res.WaitJoined)
}

var _ = exec.Command
