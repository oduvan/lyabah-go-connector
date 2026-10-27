package engine

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oduvan/lyabah-go-connector/protocol"
)

var (
	stubOnce sync.Once
	stubPath string
	stubErr  error
	stubDir  string
)

// TestMain removes the folder of the stub katago at the end.
func TestMain(m *testing.M) {
	code := m.Run()
	if stubDir != "" {
		os.RemoveAll(stubDir)
	}
	os.Exit(code)
}

// stubKatago builds testdata/stubkatago once and returns its path.
func stubKatago(t *testing.T) string {
	t.Helper()
	stubOnce.Do(func() {
		stubDir, stubErr = os.MkdirTemp("", "stubkatago")
		if stubErr != nil {
			return
		}
		stubPath = filepath.Join(stubDir, "katago")
		if runtime.GOOS == "windows" {
			stubPath += ".exe"
		}
		out, err := exec.Command("go", "build", "-o", stubPath, "./testdata/stubkatago").CombinedOutput()
		if err != nil {
			stubErr = errors.New(string(out))
		}
	})
	if stubErr != nil {
		t.Fatalf("building the stub katago: %v", stubErr)
	}
	return stubPath
}

const testBaseGTP = `# test base config
logToStderr = true
maxVisits = 100
rules = tromp-taylor
`

// newTestNative makes a native engine with the stub katago and base configs
// in a temp folder.
func newTestNative(t *testing.T, katago string) (Engine, NativeOptions) {
	t.Helper()
	dir := t.TempDir()
	gtpCfg := filepath.Join(dir, "gtp.cfg")
	analysisCfg := filepath.Join(dir, "analysis.cfg")
	if err := os.WriteFile(gtpCfg, []byte(testBaseGTP), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(analysisCfg, []byte("logToStderr = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o := NativeOptions{
		Katago:         katago,
		Model:          filepath.Join(dir, "main.bin.gz"),
		HumanModel:     filepath.Join(dir, "human.bin.gz"),
		GTPConfig:      gtpCfg,
		AnalysisConfig: analysisCfg,
		DataDir:        filepath.Join(dir, "data"),
	}
	e, err := NewNative(o)
	if err != nil {
		t.Fatal(err)
	}
	return e, o
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// waitFor polls cond for up to five seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stubArgs reads the stub's arguments from its stderr.
func stubArgs(p Process) []string {
	var args []string
	for _, line := range strings.Split(p.Stderr(), "\n") {
		if a, ok := strings.CutPrefix(line, "arg: "); ok {
			args = append(args, a)
		}
	}
	return args
}

func runFiles(t *testing.T, dataDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "run"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestNativeRefusesFileKeys(t *testing.T) {
	// A katago that does not exist: if the engine tried to start it, the
	// error would say so instead of naming the config line.
	e, o := newTestNative(t, filepath.Join(t.TempDir(), "no-katago"))
	cases := [][]string{
		{"gtp", "game-k7Qm2xPa9T", "--user", "7", "--config", b64("logFile = /etc/passwd\n")},
		{"analysis", "analysis", "--config", b64("logFile = /etc/passwd\n")},
	}
	for _, args := range cases {
		p, err := e.Start(context.Background(), args)
		if err == nil {
			p.Kill()
			t.Fatalf("Start(%q) started a process", args)
		}
		if !strings.Contains(err.Error(), "logFile is a server-level setting") {
			t.Errorf("Start(%q) = %v; want the server-level setting error", args, err)
		}
	}
	if files := runFiles(t, o.DataDir); len(files) != 0 {
		t.Errorf("files left in run: %q", files)
	}

	// Both opening folders at once are refused with the wrapper's message.
	both := "autoAvoidRepeatDir = /players/7/avoid-repeat\navoidSgfPatternDirs = /players/7/sgf\n"
	_, err := e.Start(context.Background(), []string{"gtp", "game-k7Qm2xPa9T", "--user", "7", "--config", b64(both)})
	want := "autoAvoidRepeatDir and avoidSgfPatternDirs cannot both be set: KataGo refuses to start with both"
	if err == nil || err.Error() != want {
		t.Errorf("Start with both folders = %v; want %q", err, want)
	}
}

func TestRefusesUnknownCommands(t *testing.T) {
	native, _ := newTestNative(t, filepath.Join(t.TempDir(), "no-katago"))
	command, err := NewCommand(CommandOptions{Command: filepath.Join(t.TempDir(), "no-wrapper")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, e := range map[string]Engine{"native": native, "command": command} {
		if _, err := e.Start(ctx, []string{"cleanup"}); !errors.Is(err, protocol.ErrBadCommand) {
			t.Errorf("%s: Start(cleanup) = %v; want ErrBadCommand", name, err)
		}
		if _, err := e.Start(ctx, []string{"gtp", "game-x"}); !errors.Is(err, protocol.ErrBadCommand) {
			t.Errorf("%s: Start(gtp without --user) = %v; want ErrBadCommand", name, err)
		}
		if _, err := e.Run(ctx, []string{"gtp", "game-x", "--user", "7"}, nil); !errors.Is(err, protocol.ErrBadCommand) {
			t.Errorf("%s: Run(gtp) = %v; want ErrBadCommand", name, err)
		}
		if _, err := e.Run(ctx, []string{"rm"}, nil); !errors.Is(err, protocol.ErrBadCommand) {
			t.Errorf("%s: Run(rm) = %v; want ErrBadCommand", name, err)
		}
		if _, err := e.Run(ctx, []string{"del-user", "../7"}, nil); !errors.Is(err, protocol.ErrBadCommand) {
			t.Errorf("%s: Run(del-user ../7) = %v; want ErrBadCommand", name, err)
		}
	}
}

func TestNativeGTP(t *testing.T) {
	e, o := newTestNative(t, stubKatago(t))
	game := "maxVisits = 10\nautoAvoidRepeatDir = /players/7/avoid-repeat\n"
	p, err := e.Start(context.Background(), []string{"gtp", "game-k7Qm2xPa9T", "--user", "7", "--config", b64(game)})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Kill()

	// GTP goes through stdin and stdout.
	if _, err := io.WriteString(p.Stdin(), "version\n"); err != nil {
		t.Fatal(err)
	}
	out := bufio.NewReader(p.Stdout())
	line, err := out.ReadString('\n')
	if err != nil || line != "= 1.18.2\n" {
		t.Fatalf("answer to version = %q, %v", line, err)
	}

	// The arguments.
	waitFor(t, "the stub's arguments", func() bool { return len(stubArgs(p)) >= 7 })
	args := stubArgs(p)
	if len(args) != 7 || args[0] != "gtp" || args[1] != "-config" || args[3] != "-model" || args[4] != o.Model ||
		args[5] != "-human-model" || args[6] != o.HumanModel {
		t.Fatalf("katago args = %q", args)
	}
	cfgFile := args[2]
	if filepath.Dir(cfgFile) != filepath.Join(o.DataDir, "run") {
		t.Errorf("config file %s is not in the run folder", cfgFile)
	}

	// The merged config while KataGo runs.
	data, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatal(err)
	}
	avoid := filepath.Join(o.DataDir, "players", "7", "avoid-repeat")
	want := "# test base config\nlogToStderr = true\nrules = tromp-taylor\n\n" +
		"# ---------------------------------------------------------------- from the app\n" +
		"maxVisits = 10\nautoAvoidRepeatDir = " + avoid + "\n"
	if string(data) != want {
		t.Errorf("merged config =\n%s\nwant\n%s", data, want)
	}

	// The player's two folders.
	for _, d := range []string{"avoid-repeat", "sgf"} {
		if fi, err := os.Stat(filepath.Join(o.DataDir, "players", "7", d)); err != nil || !fi.IsDir() {
			t.Errorf("folder %s: %v", d, err)
		}
	}

	// Closing stdin ends it; the merged config goes away.
	p.Stdin().Close()
	io.Copy(io.Discard, out)
	if err := p.Wait(); err != nil {
		t.Errorf("Wait = %v; want nil", err)
	}
	if _, err := os.Stat(cfgFile); !os.IsNotExist(err) {
		t.Errorf("merged config still there: %v", err)
	}

	// --no-human leaves the human network out.
	p, err = e.Start(context.Background(), []string{"gtp", "game-k7Qm2xPa9T", "--user", "7", "--no-human"})
	if err != nil {
		t.Fatal(err)
	}
	p.Stdin().Close()
	io.Copy(io.Discard, p.Stdout())
	if err := p.Wait(); err != nil {
		t.Errorf("Wait = %v", err)
	}
	if args := stubArgs(p); len(args) != 5 || args[4] != o.Model {
		t.Errorf("katago args with --no-human = %q", args)
	}

	// The analysis engine.
	p, err = e.Start(context.Background(), []string{"analysis", "analysis", "--config", b64("maxVisits = 5\n")})
	if err != nil {
		t.Fatal(err)
	}
	p.Stdin().Close()
	io.Copy(io.Discard, p.Stdout())
	if err := p.Wait(); err != nil {
		t.Errorf("Wait = %v", err)
	}
	if args := stubArgs(p); len(args) != 6 || args[0] != "analysis" || args[4] != o.Model || args[5] != "-quit-without-waiting" {
		t.Errorf("katago args for analysis = %q", args)
	}

	// Kill ends a running process, and Wait says so.
	p, err = e.Start(context.Background(), []string{"analysis", "analysis"})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Kill(); err != nil {
		t.Errorf("Kill = %v", err)
	}
	io.Copy(io.Discard, p.Stdout())
	if err := p.Wait(); err == nil || !strings.Contains(err.Error(), "killed") {
		t.Errorf("Wait after Kill = %v; want killed", err)
	}

	// The version.
	v, err := e.Version(context.Background())
	if err != nil || v != "katago: v1.18.2" {
		t.Errorf("Version = %q, %v", v, err)
	}
	full, err := e.Run(context.Background(), []string{"version"}, nil)
	wantFull := "katago: v1.18.2\nbackend: native\nmain network: main.bin.gz\nhuman network: human.bin.gz\n"
	if err != nil || string(full) != wantFull {
		t.Errorf("Run(version) = %q, %v; want %q", full, err, wantFull)
	}

	// Cleanup removes what is left in the run folder.
	left := filepath.Join(o.DataDir, "run", "gtp-old.123.cfg")
	if err := os.WriteFile(left, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if files := runFiles(t, o.DataDir); len(files) != 0 {
		t.Errorf("files left in run after Cleanup: %q", files)
	}
}

func TestNativeSGF(t *testing.T) {
	e, o := newTestNative(t, filepath.Join(t.TempDir(), "no-katago"))
	ctx := context.Background()
	run := func(stdin string, args ...string) (string, error) {
		out, err := e.Run(ctx, args, []byte(stdin))
		return string(out), err
	}
	sgfDir := filepath.Join(o.DataDir, "players", "7", "sgf")

	// put-sgf and list-sgf.
	if _, err := run("(;GM[1])", "put-sgf", "7", "k7Qm2xPa9T"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("(;GM[1]SZ[9])", "put-sgf", "7", "a1b2c3d4e5"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(sgfDir, "k7Qm2xPa9T.sgf"))
	if err != nil || string(data) != "(;GM[1])" {
		t.Errorf("saved SGF = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(o.DataDir, "players", "7", "avoid-repeat")); err != nil {
		t.Errorf("put-sgf did not make the avoid-repeat folder: %v", err)
	}
	// Files that are not a game's SGF are not listed.
	for _, name := range []string{"notes.txt", "short.sgf"} {
		if err := os.WriteFile(filepath.Join(sgfDir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := run("", "list-sgf", "7"); err != nil || out != "a1b2c3d4e5\nk7Qm2xPa9T\n" {
		t.Errorf("list-sgf = %q, %v", out, err)
	}
	if out, err := run("", "list-sgf", "8"); err != nil || out != "" {
		t.Errorf("list-sgf of a user with no folder = %q, %v", out, err)
	}

	// The size limit.
	big := strings.Repeat("x", maxSGF+1)
	if _, err := run(big, "put-sgf", "7", "BIGgame123"); err == nil || !strings.Contains(err.Error(), "bigger than 5242880 bytes") {
		t.Errorf("put-sgf of a too big SGF = %v", err)
	}
	if out, _ := run("", "list-sgf", "7"); out != "a1b2c3d4e5\nk7Qm2xPa9T\n" {
		t.Errorf("list-sgf after a refused put-sgf = %q", out)
	}
	if _, err := run(strings.Repeat("x", maxSGF), "put-sgf", "7", "BIGgame123"); err != nil {
		t.Errorf("put-sgf of exactly %d bytes = %v", maxSGF, err)
	}

	// del-sgf.
	if _, err := run("", "del-sgf", "7", "BIGgame123"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("", "del-sgf", "7", "BIGgame123"); err != nil {
		t.Errorf("del-sgf of a missing SGF = %v", err)
	}
	if out, _ := run("", "list-sgf", "7"); out != "a1b2c3d4e5\nk7Qm2xPa9T\n" {
		t.Errorf("list-sgf after del-sgf = %q", out)
	}

	// del-user.
	if _, err := run("(;GM[1])", "put-sgf", "8", "k7Qm2xPa9T"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("", "del-user", "8"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(o.DataDir, "players", "8")); !os.IsNotExist(err) {
		t.Errorf("del-user left the folder: %v", err)
	}

	// sync-users keeps the listed users and folders not named like a user id.
	for _, id := range []string{"9", "10"} {
		if _, err := run("(;GM[1])", "put-sgf", id, "k7Qm2xPa9T"); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"lost+found", "007"} {
		if err := os.MkdirAll(filepath.Join(o.DataDir, "players", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := run("\n  \n", "sync-users"); err == nil || !strings.Contains(err.Error(), "refusing an empty list of users") {
		t.Errorf("sync-users with an empty list = %v", err)
	}
	if _, err := run("7\nabc\n", "sync-users"); err == nil || !strings.Contains(err.Error(), "bad user ID") {
		t.Errorf("sync-users with a bad id = %v", err)
	}
	out, err := run(" 7 \n10", "sync-users")
	if err != nil || out != "removed 1 folders\n" {
		t.Errorf("sync-users = %q, %v", out, err)
	}
	entries, err := os.ReadDir(filepath.Join(o.DataDir, "players"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "007 10 7 lost+found" {
		t.Errorf("players after sync-users = %q", names)
	}
}

func TestNativeDataDirIsAbsolute(t *testing.T) {
	t.Chdir(t.TempDir())
	e, err := NewNative(NativeOptions{
		Katago: "katago", Model: "m.bin.gz", GTPConfig: "gtp.cfg", AnalysisConfig: "analysis.cfg",
		DataDir: "data",
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := e.(*nativeEngine).o.DataDir
	if !filepath.IsAbs(dir) || filepath.Base(dir) != "data" {
		t.Errorf("DataDir = %q; want an absolute path that ends in data", dir)
	}
}

func TestNativeSameNameReplacesTheOldProcess(t *testing.T) {
	e, _ := newTestNative(t, stubKatago(t))
	ctx := context.Background()
	start := func(name string) Process {
		t.Helper()
		p, err := e.Start(ctx, []string{"gtp", name, "--user", "7", "--no-human"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { p.Kill() })
		return p
	}
	old := start("game-k7Qm2xPa9T")
	other := start("game-a1b2c3d4e5")

	// The old process of the same name is killed before the new one starts.
	ended := make(chan error, 1)
	go func() {
		io.Copy(io.Discard, old.Stdout())
		ended <- old.Wait()
	}()
	p := start("game-k7Qm2xPa9T")
	select {
	case err := <-ended:
		if err == nil || !strings.Contains(err.Error(), "killed") {
			t.Errorf("old process Wait = %v; want killed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the old process of the same name still runs")
	}

	// The new one and the one with another name both answer.
	for _, q := range []Process{p, other} {
		if _, err := io.WriteString(q.Stdin(), "version\n"); err != nil {
			t.Fatal(err)
		}
		line, err := bufio.NewReader(q.Stdout()).ReadString('\n')
		if err != nil || line != "= 1.18.2\n" {
			t.Errorf("answer to version = %q, %v", line, err)
		}
	}
}
