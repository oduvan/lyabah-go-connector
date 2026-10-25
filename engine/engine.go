// Package engine runs the commands of the Jetson's wrapper
// (jetson/goban-katago in the Goban repository) on this computer. In
// command mode it calls a wrapper-compatible command. In native mode it
// does the wrapper's work itself and starts a katago program directly.
//
// Only the wrapper's commands with the wrapper's argument patterns are run
// (package protocol checks them). No argument from the server reaches a
// shell.
package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Process is one running KataGo (gtp or analysis).
type Process interface {
	// Stdin is KataGo's stdin. Closing it asks KataGo to quit.
	Stdin() io.WriteCloser
	// Stdout is KataGo's stdout. Read it to its end before calling Wait.
	Stdout() io.Reader
	// Stderr is the last 4096 bytes KataGo wrote to stderr so far.
	Stderr() string
	// Wait waits for the process to end. It returns nil when the process
	// exited with status 0, else an error that says how it ended (its exit
	// status, or that it was killed).
	Wait() error
	// Kill ends the process and every process it started, at once.
	Kill() error
}

// Engine runs the wrapper's commands.
type Engine interface {
	// Start starts gtp or analysis (args as protocol.CheckProcessArgs
	// takes them, the command first). The process is killed when ctx ends.
	Start(ctx context.Context, args []string) (Process, error)
	// Run runs one short command (args as protocol.CheckShortArgs takes
	// them) with stdin as its input, and returns what it printed.
	Run(ctx context.Context, args []string, stdin []byte) (stdout []byte, err error)
	// Cleanup removes what earlier processes left behind. The connector
	// runs it before each connection, when none of its processes runs.
	Cleanup(ctx context.Context) error
	// Version is the first line of the version command's output, like
	// "katago: v1.18.2".
	Version(ctx context.Context) (string, error)
}

// stderrTail is how much of a process's stderr is kept.
const stderrTail = 4096

// waitDelay is how long Wait waits for the output pipes to close after the
// process itself has ended (a child it left running may hold them open).
const waitDelay = 2 * time.Second

// tail keeps the last bytes written to it.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > stderrTail {
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-stderrTail:]...)
	}
	return len(p), nil
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// process is a Process started with exec.
type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stderr *tail
	// done runs once the process has ended, for example to delete its
	// config file.
	done func()

	waitOnce sync.Once
	waitErr  error

	mu     sync.Mutex
	waited bool // Wait has returned: the process group may be gone
}

func (p *process) Stdin() io.WriteCloser { return p.stdin }
func (p *process) Stdout() io.Reader     { return p.stdout }
func (p *process) Stderr() string        { return p.stderr.String() }

func (p *process) Wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
		p.mu.Lock()
		p.waited = true
		p.mu.Unlock()
		if p.done != nil {
			p.done()
		}
	})
	return p.waitErr
}

// Kill kills the process group. After Wait has returned it does nothing,
// because the group's number may already belong to other processes.
func (p *process) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waited {
		return nil
	}
	return killGroup(p.cmd)
}

// command makes the exec.Cmd for name args…, in a process group of its own
// that is killed when ctx ends. env is added to this program's environment.
func command(ctx context.Context, name string, args []string, env []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}
	ownGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = waitDelay
	return cmd
}

// startProcess starts cmd with pipes for stdin and stdout. done runs once
// the process has ended (also when it could not start).
func startProcess(cmd *exec.Cmd, done func()) (Process, error) {
	p := &process{cmd: cmd, stderr: &tail{}, done: done}
	cmd.Stderr = p.stderr
	fail := func(err error) (Process, error) {
		if done != nil {
			done()
		}
		return nil, err
	}
	var err error
	if p.stdin, err = cmd.StdinPipe(); err != nil {
		return fail(err)
	}
	if p.stdout, err = cmd.StdoutPipe(); err != nil {
		return fail(err)
	}
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	return p, nil
}

// runCommand runs cmd with stdin as its input and returns its stdout. When
// it fails, the error has its exit status and the end of its stderr.
func runCommand(cmd *exec.Cmd, what string, stdin []byte) ([]byte, error) {
	var out bytes.Buffer
	errOut := &tail{}
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout = &out
	cmd.Stderr = errOut
	if err := cmd.Run(); err != nil {
		if msg := bytes.TrimSpace([]byte(errOut.String())); len(msg) > 0 {
			return out.Bytes(), fmt.Errorf("%s: %w: %s", what, err, msg)
		}
		return out.Bytes(), fmt.Errorf("%s: %w", what, err)
	}
	return out.Bytes(), nil
}

// firstLine is the first line of out, without the line end.
func firstLine(out []byte) string {
	line, _, _ := bytes.Cut(out, []byte("\n"))
	return string(bytes.TrimRight(line, "\r"))
}
