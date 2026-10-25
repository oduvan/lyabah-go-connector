package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/oduvan/lyabah-go-connector/protocol"
)

// NativeOptions are the settings of native mode.
type NativeOptions struct {
	// Katago is the katago program.
	Katago string
	// Model is the main network; HumanModel the human network (empty for
	// none).
	Model      string
	HumanModel string
	// GTPConfig and AnalysisConfig are the base configs the game's
	// settings are merged into.
	GTPConfig      string
	AnalysisConfig string
	// DataDir holds the players' folders (players/<ID>/…) and the merged
	// configs of running processes (run/).
	DataDir string
}

// nativeEngine does the wrapper's work itself, without Docker, and starts
// katago directly.
type nativeEngine struct {
	o NativeOptions

	// startMu lets one Start run at a time, so that two starts of the same
	// name cannot both run.
	startMu sync.Mutex
	// mu guards running: the processes that run now, by name.
	mu      sync.Mutex
	running map[string]*process
}

// NewNative makes the engine of native mode.
func NewNative(o NativeOptions) (Engine, error) {
	for _, f := range []struct{ name, value string }{
		{"katago", o.Katago},
		{"a model", o.Model},
		{"a GTP config", o.GTPConfig},
		{"an analysis config", o.AnalysisConfig},
		{"a data folder", o.DataDir},
	} {
		if f.value == "" {
			return nil, errors.New("native mode needs " + f.name)
		}
	}
	// The folder must not change when the working folder does.
	dir, err := filepath.Abs(o.DataDir)
	if err != nil {
		return nil, err
	}
	o.DataDir = dir
	return &nativeEngine{o: o, running: map[string]*process{}}, nil
}

// runDir holds the merged configs of running processes.
func (e *nativeEngine) runDir() string { return filepath.Join(e.o.DataDir, "run") }

func (e *nativeEngine) Start(ctx context.Context, args []string) (Process, error) {
	if err := protocol.CheckProcessArgs(args); err != nil {
		return nil, err
	}
	// The args passed the check, so every option is complete here.
	mode, name := args[0], args[1]
	user, human, b64 := "", true, ""
	for i := 2; i < len(args); i++ {
		switch args[i] {
		case "--user":
			user = args[i+1]
			i++
		case "--no-human":
			human = false
		case "--config":
			b64 = args[i+1]
			i++
		}
	}
	text, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, errors.New("--config is not valid base64")
	}
	game, err := CheckConfig(string(text), mode, user)
	if err != nil {
		return nil, err
	}

	base := e.o.AnalysisConfig
	if mode == "gtp" {
		// KataGo 1.18 refuses to start with both opening folders.
		if hasKey(game, avoidRepeatKey) && hasKey(game, sgfPatternKey) {
			return nil, errBothFolders
		}
		game = e.playerFolders(game, user)
		if err := e.makeUserDirs(user); err != nil {
			return nil, err
		}
		base = e.o.GTPConfig
	}
	baseText, err := os.ReadFile(base)
	if err != nil {
		return nil, err
	}
	cfgFile, err := e.writeConfig(mode+"-"+name, MergeConfig(string(baseText), game))
	if err != nil {
		return nil, err
	}

	katagoArgs := []string{mode, "-config", cfgFile, "-model", e.o.Model}
	switch {
	case mode == "analysis":
		// When stdin closes, KataGo stops at once instead of finishing the
		// requests still queued.
		katagoArgs = append(katagoArgs, "-quit-without-waiting")
	case human && e.o.HumanModel != "":
		katagoArgs = append(katagoArgs, "-human-model", e.o.HumanModel)
	}

	// Like the wrapper, which removes a container of the same name before
	// it starts a new one: a process of the same name that still runs is
	// killed first.
	e.startMu.Lock()
	defer e.startMu.Unlock()
	e.mu.Lock()
	old := e.running[name]
	delete(e.running, name)
	e.mu.Unlock()
	if old != nil {
		old.Kill()
		old.Wait()
	}

	var p *process
	done := func() {
		os.Remove(cfgFile)
		e.mu.Lock()
		if p != nil && e.running[name] == p {
			delete(e.running, name)
		}
		e.mu.Unlock()
	}
	started, err := startProcess(command(ctx, e.o.Katago, katagoArgs, nil), done)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	p = started.(*process)
	e.running[name] = p
	e.mu.Unlock()
	return p, nil
}

// hasKey says whether the checked lines of game set key.
func hasKey(game, key string) bool {
	for _, line := range splitLines(game) {
		if strings.HasPrefix(line, key+" = ") {
			return true
		}
	}
	return false
}

// playerFolders turns the checked folder lines /players/<ID>/… into the
// user's folders in DataDir.
func (e *nativeEngine) playerFolders(game, user string) string {
	lines := splitLines(game)
	for i, line := range lines {
		switch line {
		case avoidRepeatKey + " = /players/" + user + "/avoid-repeat":
			lines[i] = avoidRepeatKey + " = " + filepath.Join(e.userDir(user), "avoid-repeat")
		case sgfPatternKey + " = /players/" + user + "/sgf":
			lines[i] = sgfPatternKey + " = " + filepath.Join(e.userDir(user), "sgf")
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// writeConfig writes a merged config to a new file in the run folder and
// returns its path.
func (e *nativeEngine) writeConfig(prefix, text string) (string, error) {
	if err := os.MkdirAll(e.runDir(), dirMode); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(e.runDir(), prefix+".*.cfg")
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func (e *nativeEngine) Run(ctx context.Context, args []string, stdin []byte) ([]byte, error) {
	if err := protocol.CheckShortArgs(args); err != nil {
		return nil, err
	}
	switch args[0] {
	case "put-sgf":
		return nil, e.putSGF(args[1], args[2], stdin)
	case "del-sgf":
		return nil, e.delSGF(args[1], args[2])
	case "del-user":
		return nil, e.delUser(args[1])
	case "sync-users":
		return e.syncUsers(stdin)
	case "list-sgf":
		return e.listSGF(args[1])
	case "version":
		return e.version(ctx)
	}
	return nil, protocol.ErrBadCommand
}

// version prints KataGo's version, the backend and the networks, like the
// wrapper's version.
func (e *nativeEngine) version(ctx context.Context) ([]byte, error) {
	out, err := runCommand(command(ctx, e.o.Katago, []string{"version"}, nil), "katago version", nil)
	if err != nil {
		return nil, err
	}
	line := firstLine(out)
	if rest, ok := strings.CutPrefix(line, "KataGo "); ok {
		line = "katago: " + rest
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\n", line)
	fmt.Fprintf(&b, "backend: native\n")
	fmt.Fprintf(&b, "main network: %s\n", filepath.Base(e.o.Model))
	human := "none"
	if e.o.HumanModel != "" {
		human = filepath.Base(e.o.HumanModel)
	}
	fmt.Fprintf(&b, "human network: %s\n", human)
	return b.Bytes(), nil
}

func (e *nativeEngine) Version(ctx context.Context) (string, error) {
	out, err := e.version(ctx)
	if err != nil {
		return "", err
	}
	return firstLine(out), nil
}

// Cleanup removes the merged configs left in the run folder by processes
// that did not end cleanly.
func (e *nativeEngine) Cleanup(ctx context.Context) error {
	entries, err := os.ReadDir(e.runDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(e.runDir(), entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
