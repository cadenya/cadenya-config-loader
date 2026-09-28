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
	err := run(ctx, command.Version(version))
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

func run(ctx context.Context, version string) error {
	// The GitHub Action runs `cadenya-config github-action` and passes its
	// inputs as INPUT_* environment variables. It isn't a listed command.
	if len(os.Args) > 1 && os.Args[1] == "github-action" {
		if len(os.Args) > 2 {
			return command.Usagef("github-action takes its inputs from INPUT_* environment variables, not arguments")
		}
		return action.Run(ctx, version, os.Getenv, os.Stdout, os.Stderr)
	}
	return command.New(version, os.Stdout, os.Stderr).Run(ctx, os.Args)
}
