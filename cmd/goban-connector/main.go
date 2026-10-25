// Command goban-connector runs KataGo on this computer and connects it to
// the Goban web app (go.lyabah.com) over one outbound WebSocket. See the
// README for how to set it up.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/oduvan/lyabah-go-connector/client"
	"github.com/oduvan/lyabah-go-connector/engine"
)

// version is the program's version. The release build sets it with
// -ldflags "-X main.version=…".
var version = "dev"

// The exit codes.
const (
	exitFailed       = 1 // the command failed
	exitUsage        = 2 // the command line is wrong
	exitUnauthorized = 3 // Goban does not take the token
	exitReplaced     = 4 // another connector with the same token took over
)

const usage = `Usage:
  goban-connector pair CODE [--server URL]
      Pair this computer with your Goban account. Get CODE with the
      "Connect a computer" button on the Goban website.
  goban-connector init --command PATH [--env KEY=VALUE]...
  goban-connector init --katago PATH --model FILE [--human-model FILE]
                       [--gtp-config FILE] [--analysis-config FILE]
                       [--data-dir DIR] [--threads N]
      Tell the connector how to run KataGo.
  goban-connector run
      Connect to Goban and serve KataGo until you stop it (Ctrl-C).
  goban-connector version
      Show the version.

The config file is $GOBAN_CONNECTOR_CONFIG, or goban-connector/config.json
in your user config folder.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run runs the command in args and gives the exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	rest := args[1:]
	switch args[0] {
	case "pair":
		return pairCommand(ctx, rest, stdout, stderr)
	case "init":
		return initCommand(rest, stdout, stderr)
	case "run":
		return runCommand(ctx, rest, stderr)
	case "version", "--version", "-version":
		fmt.Fprintln(stdout, version)
		return 0
	case "help", "--help", "-h", "-help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "Unknown command %q.\n\n%s", args[0], usage)
	return exitUsage
}

// newFlagSet makes the flag set of one command. Its own messages go to w.
func newFlagSet(name string, w io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet("goban-connector "+name, flag.ContinueOnError)
	fs.SetOutput(w)
	return fs
}

// parseFlags parses args, which may have flags before and after the
// positional arguments (the README writes "pair CODE --server URL"). It
// returns the positional arguments. A -h gives the exit code 0.
func parseFlags(fs *flag.FlagSet, args []string) (positional []string, code int, ok bool) {
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, 0, false
			}
			return nil, exitUsage, false
		}
		if fs.NArg() == 0 {
			return positional, 0, true
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// stringList is a flag that can be given many times.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// loadOwnConfig finds and reads the config file.
func loadOwnConfig(stderr io.Writer) (path string, c Config, ok bool) {
	path, err := configPath()
	if err == nil {
		c, err = loadConfig(path)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return "", Config{}, false
	}
	return path, c, true
}

// pairCommand pairs this computer with a Goban account and saves the token.
func pairCommand(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("pair", stderr)
	server := fs.String("server", "", "Goban's address (default: the config's server, else "+client.DefaultServer+")")
	codes, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if len(codes) != 1 {
		fmt.Fprintln(stderr, "Usage: goban-connector pair CODE [--server URL]")
		return exitUsage
	}

	path, cfg, ok := loadOwnConfig(stderr)
	if !ok {
		return exitFailed
	}
	if *server != "" {
		cfg.Server = *server
	}
	// Goban keeps the name given on the website. The host name only goes
	// into the connector's info.
	hostname, _ := os.Hostname()
	res, err := client.Pair(ctx, cfg.server(), codes[0], client.PairInfo{
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Version:  version,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}

	cfg.Server = cfg.server()
	cfg.Token, cfg.ConnectorID, cfg.Name = res.Token, res.ConnectorID, res.Name
	if err := saveConfig(path, cfg); err != nil {
		fmt.Fprintf(stderr, "Paired, but could not save the config file %s: %v\n", path, err)
		return exitFailed
	}
	fmt.Fprintf(stdout, "Paired as %s. Start it with: goban-connector run\n", res.Name)
	return 0
}

// initCommand writes the engine part of the config.
func initCommand(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("init", stderr)
	command := fs.String("command", "", "command mode: a wrapper-compatible command")
	var env stringList
	fs.Var(&env, "env", "command mode: KEY=VALUE for the command (can be given many times)")
	katago := fs.String("katago", "", "native mode: the katago program")
	model := fs.String("model", "", "native mode: the network file")
	humanModel := fs.String("human-model", "", "native mode: the human network file")
	gtpConfig := fs.String("gtp-config", "", "native mode: base config for GTP games (default: one is written)")
	analysisConfig := fs.String("analysis-config", "", "native mode: base config for analysis (default: one is written)")
	dataDir := fs.String("data-dir", "", "native mode: folder for players' files (default: data next to the config file)")
	threads := fs.Int("threads", 4, "native mode: search threads in the default base configs")
	_, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	bad := func(msg string) int {
		fmt.Fprintln(stderr, msg)
		return exitUsage
	}

	if fs.NArg() > 0 {
		return bad("init takes only flags, but got " + fs.Arg(0))
	}
	native := *katago != ""
	switch {
	case *command != "" && native:
		return bad("Give --command or --katago, not both.")
	case *command == "" && !native:
		return bad("Give --command PATH, or --katago PATH with --model FILE.\n\n" + usage)
	}

	path, cfg, ok := loadOwnConfig(stderr)
	if !ok {
		return exitFailed
	}
	// The engine part is written whole: the settings of the other mode go.
	cfg.Mode, cfg.Command, cfg.Env = "", "", nil
	cfg.Katago, cfg.Model, cfg.HumanModel, cfg.GTPConfig, cfg.AnalysisConfig, cfg.DataDir = "", "", "", "", "", ""

	var written []string // base configs this command writes
	if !native {
		for _, f := range []string{"model", "human-model", "gtp-config", "analysis-config", "data-dir", "threads"} {
			if given[f] {
				return bad("--" + f + " is for native mode (--katago).")
			}
		}
		if _, err := engine.NewCommand(engine.CommandOptions{Command: *command, Env: env}); err != nil {
			return bad(err.Error())
		}
		cfg.Mode, cfg.Env = "command", env
		var err error
		if cfg.Command, err = absPath(*command, true); err != nil {
			return bad(err.Error())
		}
	} else {
		if *model == "" {
			return bad("Native mode needs --model FILE.")
		}
		if *threads < 1 {
			return bad("--threads must be 1 or more.")
		}
		if len(env) > 0 {
			return bad("--env is for command mode (--command).")
		}
		cfg.Mode = "native"
		dir := filepath.Dir(path)
		for _, f := range []struct {
			dst  *string
			from string
			kind string
		}{
			{&cfg.Katago, *katago, ""},
			{&cfg.Model, *model, ""},
			{&cfg.HumanModel, *humanModel, ""},
			{&cfg.GTPConfig, *gtpConfig, "gtp"},
			{&cfg.AnalysisConfig, *analysisConfig, "analysis"},
			{&cfg.DataDir, *dataDir, ""},
		} {
			from := f.from
			if from == "" && f.kind != "" {
				from = filepath.Join(dir, f.kind+".cfg")
				written = append(written, f.kind)
			}
			var err error
			if *f.dst, err = absPath(from, f.dst == &cfg.Katago); err != nil {
				return bad(err.Error())
			}
		}
		if _, err := engine.NewNative(engine.NativeOptions{Katago: cfg.Katago, Model: cfg.Model,
			GTPConfig: cfg.GTPConfig, AnalysisConfig: cfg.AnalysisConfig, DataDir: cfg.dataDir(path)}); err != nil {
			return bad(err.Error())
		}
	}

	// All settings are checked. Now files are written.
	for _, kind := range written {
		file := cfg.GTPConfig
		if kind == "analysis" {
			file = cfg.AnalysisConfig
		}
		if _, err := os.Stat(file); err == nil {
			fmt.Fprintf(stdout, "Kept the base config %s. Delete it and run init again to get a new one.\n", file)
			continue
		}
		if err := writeFile(file, baseConfig(kind, *threads)); err != nil {
			fmt.Fprintf(stderr, "Could not write the base config %s: %v\n", file, err)
			return exitFailed
		}
		fmt.Fprintf(stdout, "Wrote the base config %s.\n", file)
	}
	if err := saveConfig(path, cfg); err != nil {
		fmt.Fprintf(stderr, "Could not save the config file %s: %v\n", path, err)
		return exitFailed
	}
	warnAboutMissingFiles(cfg, stderr)
	fmt.Fprintf(stdout, "Saved the engine settings in %s.\n", path)
	if cfg.Token == "" {
		fmt.Fprintln(stdout, "Next, pair this computer: goban-connector pair CODE")
	} else {
		fmt.Fprintln(stdout, "Start it with: goban-connector run")
	}
	return 0
}

// writeFile writes a new file with mode 0600, and its folder (when it is
// new) with 0700.
func writeFile(path, text string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o600)
}

// warnAboutMissingFiles tells the user about a program or a file that the
// config names and that is not there. It is only a warning: the file may
// come later.
func warnAboutMissingFiles(c Config, stderr io.Writer) {
	program := c.Katago
	if c.Mode == "command" {
		program = c.Command
	}
	if _, err := exec.LookPath(program); err != nil {
		fmt.Fprintf(stderr, "Warning: I cannot find the program %s.\n", program)
	}
	for _, f := range []string{c.Model, c.HumanModel} {
		if f == "" {
			continue
		}
		if _, err := os.Stat(f); err != nil {
			fmt.Fprintf(stderr, "Warning: I cannot find the file %s.\n", f)
		}
	}
}

// runCommand connects to Goban and serves KataGo until ctx ends.
func runCommand(ctx context.Context, args []string, stderr io.Writer) int {
	fs := newFlagSet("run", stderr)
	_, code, ok := parseFlags(fs, args)
	if !ok {
		return code
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "run takes no arguments.")
		return exitUsage
	}
	path, cfg, ok := loadOwnConfig(stderr)
	if !ok {
		return exitFailed
	}
	if cfg.Token == "" {
		fmt.Fprintln(stderr, "This computer is not paired yet. Pair it first: goban-connector pair CODE")
		return exitFailed
	}
	eng, err := buildEngine(cfg, path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitFailed
	}

	log := slog.New(slog.NewTextHandler(stderr, nil))
	log.Info("goban-connector starts", "version", version, "server", cfg.server(), "mode", cfg.Mode)
	err = client.Run(ctx, client.Options{
		Server:   cfg.server(),
		Token:    cfg.Token,
		Engine:   eng,
		Version:  version,
		Mode:     cfg.Mode,
		MaxGames: cfg.maxGames(),
		Log:      log,
	})
	return exitCode(err, stderr)
}

// buildEngine makes the engine the config asks for.
func buildEngine(c Config, path string) (engine.Engine, error) {
	notSet := errors.New("KataGo is not set up yet. Run: goban-connector init --help")
	switch c.Mode {
	case "command":
		e, err := engine.NewCommand(engine.CommandOptions{Command: c.Command, Env: c.Env})
		if err != nil {
			return nil, fmt.Errorf("%w (%v)", notSet, err)
		}
		return e, nil
	case "native":
		e, err := engine.NewNative(engine.NativeOptions{
			Katago:         c.Katago,
			Model:          c.Model,
			HumanModel:     c.HumanModel,
			GTPConfig:      c.GTPConfig,
			AnalysisConfig: c.AnalysisConfig,
			DataDir:        c.dataDir(path),
		})
		if err != nil {
			return nil, fmt.Errorf("%w (%v)", notSet, err)
		}
		return e, nil
	}
	return nil, notSet
}

// exitCode turns the error of client.Run into an exit code, and prints what
// the user needs to know.
func exitCode(err error, stderr io.Writer) int {
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		return 0
	case errors.Is(err, client.ErrUnauthorized):
		fmt.Fprintln(stderr, "The token is not valid any more. Pair again: goban-connector pair CODE")
		return exitUnauthorized
	case errors.Is(err, client.ErrReplaced):
		fmt.Fprintln(stderr, "Another goban-connector with the same token connected. This one stops.")
		return exitReplaced
	}
	fmt.Fprintln(stderr, err)
	return exitFailed
}
