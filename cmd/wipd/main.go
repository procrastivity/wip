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
	<-ctx.Done()
	if err := daemon.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "wipd: shutdown: %v\n", err)
		return 1
	}
	return 0
}
