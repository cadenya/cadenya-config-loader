package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cadenya/cadenya-config-loader/internal/action"
	"github.com/cadenya/cadenya-config-loader/internal/command"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := action.Run(ctx, command.Version(version), os.Getenv, os.Stdout, os.Stderr)
	// A signal cancels ctx. Read it before stop() so the exit code says why.
	interrupted := ctx.Err() != nil
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
	}
	if code := command.ExitCode(err, interrupted); code != command.ExitOK {
		stop()
		os.Exit(code)
	}
}
