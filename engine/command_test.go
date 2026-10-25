package engine

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testWrapper stands in for a wrapper-compatible command. It prints the
// prefix it was given and its arguments, then copies stdin to stdout with a
// child process (cat), so Kill has a child to end too.
const testWrapper = `#!/bin/sh
case "$1" in
version) echo "katago: v1.18.2"; echo "backend: test"; exit 0 ;;
cleanup) echo "removed 0 containers"; exit 0 ;;
del-user) echo "goban-katago: no such luck" >&2; exit 2 ;;
esac
echo "prefix=$GOBAN_KATAGO_PREFIX"
for a in "$@"; do echo "arg=$a"; done
cat
`

func TestCommandMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test wrapper is a shell script")
	}
	script := filepath.Join(t.TempDir(), "goban-katago")
	if err := os.WriteFile(script, []byte(testWrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	e, err := NewCommand(CommandOptions{Command: script, Env: []string{"GOBAN_KATAGO_PREFIX=gobanconn-"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Run: env and args passed, stdin piped to stdout.
	out, err := e.Run(ctx, []string{"put-sgf", "7", "k7Qm2xPa9T"}, []byte("(;GM[1])"))
	want := "prefix=gobanconn-\narg=put-sgf\narg=7\narg=k7Qm2xPa9T\n(;GM[1])"
	if err != nil || string(out) != want {
		t.Errorf("Run(put-sgf) = %q, %v; want %q", out, err, want)
	}

	// A failing command: the error carries its stderr.
	if _, err := e.Run(ctx, []string{"del-user", "7"}, nil); err == nil ||
		!strings.Contains(err.Error(), "exit status 2") || !strings.Contains(err.Error(), "goban-katago: no such luck") {
		t.Errorf("Run(del-user) = %v; want the exit status and stderr", err)
	}

	if v, err := e.Version(ctx); err != nil || v != "katago: v1.18.2" {
		t.Errorf("Version = %q, %v", v, err)
	}
	if err := e.Cleanup(ctx); err != nil {
		t.Errorf("Cleanup = %v", err)
	}

	// Start: each argument is one argv entry, even with spaces in --config's
	// decoded text (the base64 itself has none).
	cfg := b64("maxVisits = 10\n")
	p, err := e.Start(ctx, []string{"gtp", "game-k7Qm2xPa9T", "--user", "7", "--config", cfg})
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(p.Stdout())
	var lines []string
	for range 7 {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, strings.TrimSuffix(line, "\n"))
	}
	wantLines := []string{"prefix=gobanconn-", "arg=gtp", "arg=game-k7Qm2xPa9T", "arg=--user", "arg=7", "arg=--config", "arg=" + cfg}
	if strings.Join(lines, "|") != strings.Join(wantLines, "|") {
		t.Errorf("output = %q; want %q", lines, wantLines)
	}
	if _, err := io.WriteString(p.Stdin(), "version\n"); err != nil {
		t.Fatal(err)
	}
	if line, err := r.ReadString('\n'); err != nil || line != "version\n" {
		t.Errorf("echo = %q, %v", line, err)
	}

	// Kill ends the script and its child: stdout reaches its end.
	if err := p.Kill(); err != nil {
		t.Errorf("Kill = %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, r)
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stdout still open after Kill: the child process lives on")
	}
	if err := p.Wait(); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Errorf("Wait after Kill = %v; want killed", err)
	}

	// Ending the context of Start ends the process too.
	ctx2, cancel := context.WithCancel(ctx)
	p, err = e.Start(ctx2, []string{"analysis", "analysis"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	io.Copy(io.Discard, p.Stdout())
	if err := p.Wait(); err == nil {
		t.Error("Wait after the context ended = nil; want an error")
	}

	// Closing stdin lets it end by itself with status 0.
	p, err = e.Start(ctx, []string{"analysis", "analysis"})
	if err != nil {
		t.Fatal(err)
	}
	p.Stdin().Close()
	io.Copy(io.Discard, p.Stdout())
	if err := p.Wait(); err != nil {
		t.Errorf("Wait after stdin closed = %v; want nil", err)
	}

	// A command that is not there.
	if _, err := NewCommand(CommandOptions{}); err == nil {
		t.Error("NewCommand without a command = nil error")
	}
	if _, err := NewCommand(CommandOptions{Command: script, Env: []string{"NOEQUALS"}}); err == nil {
		t.Error("NewCommand with a bad env entry = nil error")
	}
	missing, _ := NewCommand(CommandOptions{Command: filepath.Join(t.TempDir(), "none")})
	if _, err := missing.Start(ctx, []string{"analysis", "analysis"}); err == nil {
		t.Errorf("Start of a missing command = %v; want an error", err)
	}
}
