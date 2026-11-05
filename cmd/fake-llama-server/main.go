// Command fake-llama-server stands in for llama-server in router mode during tests.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/Angelmmiguel/llama-gui/internal/fakellama"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	code := fakellama.Run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
