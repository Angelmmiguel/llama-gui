// Command llama-gui is a dashboard and control plane for llama.cpp: a single
// binary that installs llama-server, supervises it, and fronts it as the only
// entrypoint.
package main

import (
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: llama-gui version")
	os.Exit(2)
}
