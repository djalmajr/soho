//go:build ignore

// Run with: go run docs/formal/check.go /absolute/tla2tools.jar /new/evidence/dir
// The evidence directory must not exist. Each run preserves its full output,
// source, configuration and TLC metadata; no live transport is invoked.
package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type experiment struct{ name, marker, invariant string }

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func fresh(path string, data []byte) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(err)
	_, err = f.Write(data)
	must(err)
	must(f.Close())
}

func main() {
	if len(os.Args) != 3 || !filepath.IsAbs(os.Args[1]) || !filepath.IsAbs(os.Args[2]) {
		panic("usage: go run docs/formal/check.go /absolute/tla2tools.jar /new/evidence/dir")
	}
	jar, evidence := os.Args[1], os.Args[2]
	must(os.Mkdir(evidence, 0700))
	source, err := os.ReadFile("docs/formal/worker_lifecycle.tla")
	must(err)
	config, err := os.ReadFile("docs/formal/worker_lifecycle.cfg")
	must(err)
	fresh(filepath.Join(evidence, "inputs.sha256"), []byte(fmt.Sprintf("%x  worker_lifecycle.tla\n%x  worker_lifecycle.cfg\n", sha256.Sum256(source), sha256.Sum256(config))))
	experiments := []experiment{
		{"control-before", "", ""},
		{"uncertain", "uncertain-preserved", "UncertainPreserved"},
		{"name", "name-unique", "UniqueLiveRoster"},
		{"observed", "dispatch-observed", "DispatchObservedGuard"},
		{"send", "core-send-guard", "CoreSendGuard"},
		{"release", "release-observed", "ReleaseObservedGuard"},
		{"owner", "owned-close", "OwnedCloseOnly"},
		{"preflight", "reopen-preflight", "ReopenPreflight"},
		{"report", "wait-report", "WaitHasReport"},
		{"active-wait", "active-wait", "ActiveAssignmentOwnsCompletion"},
		{"active-release", "active-release", "ActiveAssignmentOwnsCompletion"},
		{"fairness", "wait-fairness", "ReportEventuallyObserved"},
		{"diagnostic-close", "", "NoUnreportedWorkingClose"},
		{"diagnostic-gap", "", "NoReopenGap"},
		{"control-after", "", ""},
	}
	for _, e := range experiments {
		dir := filepath.Join(evidence, e.name)
		formal := filepath.Join(dir, "docs", "formal")
		must(os.MkdirAll(formal, 0700))
		body := string(source)
		if e.marker != "" {
			lines, count := strings.Split(body, "\n"), 0
			for i, line := range lines {
				if strings.HasSuffix(line, "\\* MUTATE "+e.marker) {
					indent := line[:strings.Index(line, "/\\")]
					lines[i] = indent + "/\\ TRUE \\* removed guard: " + e.marker
					count++
				}
			}
			if count != 1 {
				panic(fmt.Sprintf("%s: expected one guard, got %d", e.name, count))
			}
			body = strings.Join(lines, "\n")
		}
		cfg := string(config)
		if e.invariant != "" {
			cfg = "SPECIFICATION Spec\nCHECK_DEADLOCK FALSE\nINVARIANT " + e.invariant + "\n"
		}
		if e.name == "fairness" {
			cfg = "SPECIFICATION Spec\nCHECK_DEADLOCK FALSE\nPROPERTY ReportEventuallyObserved\n"
		}
		if e.name == "diagnostic-close" {
			cfg = strings.Replace(cfg, "SPECIFICATION Spec", "SPECIFICATION CloseRaceSpec", 1)
			cfg += "CONSTRAINT NoCoreChange\n"
		}
		fresh(filepath.Join(formal, "worker_lifecycle.tla"), []byte(body))
		fresh(filepath.Join(formal, "worker_lifecycle.cfg"), []byte(cfg))
		fresh(filepath.Join(dir, "inputs.sha256"), []byte(fmt.Sprintf("%x  docs/formal/worker_lifecycle.tla\n%x  docs/formal/worker_lifecycle.cfg\n", sha256.Sum256([]byte(body)), sha256.Sum256([]byte(cfg)))))
		args := []string{"-XX:+UseParallelGC", "-cp", jar, "tlc2.TLC", "-workers", "2", "-config", "docs/formal/worker_lifecycle.cfg", "docs/formal/worker_lifecycle.tla"}
		fresh(filepath.Join(dir, "command.txt"), []byte("/usr/bin/java "+strings.Join(args, " ")+"\n"))
		ctx, cancel := context.WithTimeout(context.Background(), 270*time.Second)
		cmd := exec.CommandContext(ctx, "/usr/bin/java", args...)
		cmd.Dir = dir
		out, runErr := cmd.CombinedOutput()
		cancel()
		fresh(filepath.Join(dir, "tlc.log"), out)
		status := 0
		if runErr != nil {
			if ee, ok := runErr.(*exec.ExitError); ok {
				status = ee.ExitCode()
			} else {
				panic(runErr)
			}
		}
		fresh(filepath.Join(dir, "exit.txt"), []byte(fmt.Sprintln(status)))
		fmt.Printf("%s: exit=%d\n", e.name, status)
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "Error:") || strings.Contains(line, "Model checking completed") || strings.Contains(line, "states generated") || strings.Contains(line, "depth of the complete") || strings.HasPrefix(line, "Finished in") || strings.HasPrefix(line, "Checking temporal properties") || strings.HasPrefix(line, "Finished checking temporal") {
				fmt.Println(line)
			}
		}
		if e.invariant == "" {
			if status != 0 || !strings.Contains(string(out), "Model checking completed. No error has been found.") {
				panic("control failed: " + filepath.Join(dir, "tlc.log"))
			}
		} else if e.name == "fairness" {
			if status == 0 || !strings.Contains(string(out), "Error: Temporal property ReportEventuallyObserved was violated.") {
				panic("expected temporal counterexample missing: " + filepath.Join(dir, "tlc.log"))
			}
		} else if status == 0 || !strings.Contains(string(out), "Error: Invariant "+e.invariant+" is violated.") {
			panic("expected semantic counterexample missing: " + filepath.Join(dir, "tlc.log"))
		}
	}
	after, err := os.ReadFile("docs/formal/worker_lifecycle.tla")
	must(err)
	if string(after) != string(source) {
		panic("source changed during experiment")
	}
	configAfter, err := os.ReadFile("docs/formal/worker_lifecycle.cfg")
	must(err)
	if string(configAfter) != string(config) {
		panic("configuration changed during experiment")
	}
	fmt.Println("PASS: ten independent guard removals killed for nine invariants; fairness removal killed; two diagnostic expectations witnessed; source and config unchanged; fair temporal controls completed before and after")
}
