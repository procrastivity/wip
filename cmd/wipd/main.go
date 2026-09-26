// Command wipd starts the explicitly scoped local daemon in the foreground.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		acceptAuthenticated(daemon)
	}()
	<-ctx.Done()
	closeErr := daemon.Close()
	<-acceptDone
	if closeErr != nil {
		fmt.Fprintf(os.Stderr, "wipd: shutdown: %v\n", closeErr)
		return 1
	}
	return 0
}

func acceptAuthenticated(daemon *wipd.Daemon) {
	for {
		connection, err := daemon.Accept()
		if err == nil {
			_ = connection.Close()
			continue
		}
		if errors.Is(err, net.ErrClosed) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}
