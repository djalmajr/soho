package main

import (
	"fmt"
	"os"

	"github.com/djalmajr/soho/internal/cli"
	"github.com/djalmajr/soho/internal/platform"
)

func main() {
	// The native worker environment helper runs before any configuration,
	// state or transport work: it filters only its named entries and execs
	// the worker in this process.
	if len(os.Args) >= 2 && os.Args[1] == platform.WorkerEnvExecMode {
		if err := platform.WorkerEnvExec(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "soho: "+err.Error())
			os.Exit(4)
		}
		return
	}
	os.Exit(cli.Run(os.Args[1:], platform.EnvFromOS()))
}
