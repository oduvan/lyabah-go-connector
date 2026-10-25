package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/oduvan/lyabah-go-connector/protocol"
)

// CommandOptions are the settings of command mode.
type CommandOptions struct {
	// Command is a wrapper-compatible command, run as Command args….
	Command string
	// Env is added to the command's environment, as "KEY=value" entries.
	// For the Jetson's wrapper: GOBAN_KATAGO_PREFIX=gobanconn-.
	Env []string
}

// commandEngine runs a wrapper-compatible command.
type commandEngine struct {
	o CommandOptions
}

// NewCommand makes the engine of command mode.
func NewCommand(o CommandOptions) (Engine, error) {
	if o.Command == "" {
		return nil, errors.New("command mode needs a command")
	}
	for _, kv := range o.Env {
		if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
			return nil, errors.New("env entry " + kv + " is not KEY=value")
		}
	}
	return &commandEngine{o: o}, nil
}

func (e *commandEngine) Start(ctx context.Context, args []string) (Process, error) {
	if err := protocol.CheckProcessArgs(args); err != nil {
		return nil, err
	}
	return startProcess(command(ctx, e.o.Command, args, e.o.Env), nil)
}

func (e *commandEngine) Run(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	if err := protocol.CheckShortArgs(args); err != nil {
		return nil, err
	}
	return e.run(ctx, args, stdin)
}

// run runs the command with args, unchecked.
func (e *commandEngine) run(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	return runCommand(command(ctx, e.o.Command, args, e.o.Env), args[0], stdin)
}

func (e *commandEngine) Cleanup(ctx context.Context) error {
	_, err := e.run(ctx, []string{"cleanup"}, nil)
	return err
}

func (e *commandEngine) Version(ctx context.Context) (string, error) {
	out, err := e.run(ctx, []string{"version"}, nil)
	if err != nil {
		return "", err
	}
	return firstLine(out), nil
}
