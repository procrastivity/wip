// Command wipd starts the explicitly scoped local daemon in the foreground.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/procrastivity/wip/internal/wipd"
	"github.com/procrastivity/wip/internal/wipdremote"
)

func main() {
	os.Exit(run())
}

func run() int {
	profileRoot := flag.String("profile-root", "", "explicit private daemon profile root (required)")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "wipd: unexpected positional arguments")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	daemon, err := wipd.Start(*profileRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wipd: startup refused: %v\n", err)
		return 1
	}
	server, runtime, err := wipdremote.NewServer(*profileRoot)
	if err != nil {
		_ = daemon.Close()
		fmt.Fprintf(os.Stderr, "wipd: connected authority startup refused: %v\n", err)
		return 1
	}
	if runtime != nil {
		defer func() { _ = runtime.Close() }()
	}
	if err := server.Serve(ctx, daemon); err != nil {
		fmt.Fprintf(os.Stderr, "wipd: serve local protocol: %v\n", err)
		return 1
	}
	return 0
}
