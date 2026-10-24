// Command stt-sidecar hosts the local speech recognition runtime and speaks
// JSON-RPC over stdin/stdout with the core (see docs/adr/0001).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/xlsft/openclick/internal/buildinfo"
)

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(buildinfo.String())
		return
	}
	// The JSON-RPC server is implemented in OC-28.
	fmt.Fprintln(os.Stderr, "stt-sidecar: protocol not implemented yet")
	os.Exit(2)
}
