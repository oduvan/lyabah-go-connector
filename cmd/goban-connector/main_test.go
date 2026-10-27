package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/oduvan/lyabah-go-connector/client"
)

// goban runs the command with args and gives its exit code and what it
// printed.
func goban(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// useConfig points the connector at a config file in a folder that does not
// exist yet, and gives the file's path.
func useConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "goban-connector", "config.json")
	t.Setenv(configEnv, path)
	return path
}

func readConfig(t *testing.T, path string) Config {
	t.Helper()
	c, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func writeConfig(t *testing.T, path string, c Config) {
	t.Helper()
	if err := saveConfig(path, c); err != nil {
		t.Fatal(err)
	}
}

func TestConfigPath(t *testing.T) {
	t.Setenv(configEnv, "/somewhere/else.json")
	if got, err := configPath(); err != nil || got != "/somewhere/else.json" {
		t.Errorf("configPath() = %q, %v; want the environment variable", got, err)
	}

	t.Setenv(configEnv, "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	got, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("goban-connector", "config.json"); !strings.HasSuffix(got, want) {
		t.Errorf("configPath() = %q; want it to end with %q", got, want)
	}
}

func TestConfigDefaults(t *testing.T) {
	var c Config
	if c.server() != "https://go.lyabah.com" || c.maxGames() != 2 {
		t.Errorf("server = %q, maxGames = %d", c.server(), c.maxGames())
	}
	if got, want := c.dataDir(filepath.Join("a", "config.json")), filepath.Join("a", "data"); got != want {
		t.Errorf("dataDir = %q, want %q", got, want)
	}
}

func TestPairSavesConfig(t *testing.T) {
	path := useConfig(t)
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/connector/pair" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"token":"gct_secret","connectorId":7,"name":"Desk"}`))
	}))
	defer srv.Close()

	// The engine part and other fields stay.
	writeConfig(t, path, Config{Mode: "command", Command: "/bin/wrapper", Env: []string{"A=1"}, MaxGames: 3, Token: "gct_old"})

	// The code comes before the flags, like in the README.
	code, out, errOut := goban(t, "pair", "abcd-efgh", "--server", srv.URL)
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if want := "Paired as Desk. Start it with: goban-connector run\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	// The host name is always the computer's own.
	hostname, _ := os.Hostname()
	if got["code"] != "ABCDEFGH" || got["hostname"] != hostname || got["version"] != "dev" || got["os"] != runtime.GOOS {
		t.Errorf("pair request = %v", got)
	}

	c := readConfig(t, path)
	want := Config{Server: srv.URL, Token: "gct_secret", ConnectorID: 7, Name: "Desk",
		Mode: "command", Command: "/bin/wrapper", Env: []string{"A=1"}, MaxGames: 3}
	if c.Server != want.Server || c.Token != want.Token || c.ConnectorID != want.ConnectorID || c.Name != want.Name ||
		c.Mode != want.Mode || c.Command != want.Command || len(c.Env) != 1 || c.Env[0] != "A=1" || c.MaxGames != 3 {
		t.Errorf("config = %+v, want %+v", c, want)
	}
	if strings.Contains(out+errOut, "gct_secret") {
		t.Error("the token was printed")
	}

	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("config mode = %v, want 0600", fi.Mode().Perm())
		}
		di, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if di.Mode().Perm() != 0o700 {
			t.Errorf("config folder mode = %v, want 0700", di.Mode().Perm())
		}
	}
}

func TestPairFlagsBeforeCodeAndFreshFile(t *testing.T) {
	path := useConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"token":"gct_secret","connectorId":8,"name":"Laptop"}`))
	}))
	defer srv.Close()
	if code, _, errOut := goban(t, "pair", "--server", srv.URL, "ABCD-EFGH"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if c := readConfig(t, path); c.Token != "gct_secret" || c.Name != "Laptop" || c.Server != srv.URL {
		t.Errorf("config = %+v", c)
	}
}

func TestPairRefusedCodeWritesNothing(t *testing.T) {
	path := useConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"This pairing code is not valid or has expired."}`))
	}))
	defer srv.Close()
	code, _, errOut := goban(t, "pair", "ABCD-EFGH", "--server", srv.URL)
	if code != 1 || !strings.Contains(errOut, "This pairing code is not valid or has expired.") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the config file exists after a refused code: %v", err)
	}
}

func TestPairNeedsOneCode(t *testing.T) {
	useConfig(t)
	for _, args := range [][]string{{"pair"}, {"pair", "A", "B"}} {
		if code, _, errOut := goban(t, args...); code != 2 || !strings.Contains(errOut, "CODE") {
			t.Errorf("%v: exit %d, stderr %q; want exit 2 and a hint", args, code, errOut)
		}
	}
}

// pair has no --name flag: Goban keeps the name given on the website.
func TestPairHasNoNameFlag(t *testing.T) {
	useConfig(t)
	code, _, errOut := goban(t, "pair", "ABCD-EFGH", "--server", "http://127.0.0.1:1", "--name", "My PC")
	if code != 2 || !strings.Contains(errOut, "-name") {
		t.Errorf("exit %d, stderr %q; want exit 2 and the unknown flag", code, errOut)
	}
	if code, out, _ := goban(t, "help"); code != 0 || strings.Contains(out, "--name") {
		t.Errorf("help: exit %d, stdout %q; want no --name", code, out)
	}
}

func TestInitCommandMode(t *testing.T) {
	path := useConfig(t)
	writeConfig(t, path, Config{Token: "gct_keep", Name: "Desk", Mode: "native", Katago: "old", Model: "old"})
	// A full path on every system: on Windows, /usr/local/bin/… has no
	// drive, so init would make it absolute.
	wrapper := filepath.Join(t.TempDir(), "goban-katago")

	code, _, errOut := goban(t, "init", "--command", wrapper,
		"--env", "GOBAN_KATAGO_PREFIX=gobanconn-", "--env", "B=2")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	c := readConfig(t, path)
	if c.Mode != "command" || c.Command != wrapper ||
		strings.Join(c.Env, ",") != "GOBAN_KATAGO_PREFIX=gobanconn-,B=2" {
		t.Errorf("config = %+v", c)
	}
	if c.Token != "gct_keep" || c.Name != "Desk" {
		t.Errorf("the pairing was lost: %+v", c)
	}
	if c.Katago != "" || c.Model != "" {
		t.Errorf("the old native settings stayed: %+v", c)
	}
}

func TestInitNativeWritesBaseConfigs(t *testing.T) {
	path := useConfig(t)
	dir := filepath.Dir(path)
	code, _, errOut := goban(t, "init", "--katago", "katago", "--model", filepath.Join(dir, "net.bin.gz"),
		"--human-model", filepath.Join(dir, "human.bin.gz"), "--threads", "6", "--data-dir", filepath.Join(dir, "d"))
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	c := readConfig(t, path)
	if c.Mode != "native" || c.Katago != "katago" || c.Model != filepath.Join(dir, "net.bin.gz") ||
		c.HumanModel != filepath.Join(dir, "human.bin.gz") || c.DataDir != filepath.Join(dir, "d") {
		t.Errorf("config = %+v", c)
	}
	if c.GTPConfig != filepath.Join(dir, "gtp.cfg") || c.AnalysisConfig != filepath.Join(dir, "analysis.cfg") {
		t.Errorf("base configs = %q, %q", c.GTPConfig, c.AnalysisConfig)
	}

	for _, f := range []struct{ file, thread string }{
		{c.GTPConfig, "numSearchThreads = 6"},
		{c.AnalysisConfig, "numSearchThreadsPerAnalysisThread = 6"},
	} {
		data, err := os.ReadFile(f.file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, want := range []string{"ponderingEnabled = false", "reportAnalysisWinratesAs = BLACK",
			"logToStderr = true", "logAllGTPCommunication = false", f.thread} {
			if !strings.Contains(text, want+"\n") {
				t.Errorf("%s has no line %q:\n%s", f.file, want, text)
			}
		}
	}
}

func TestInitNativeDefaultThreadsAndKeepsEditedConfigs(t *testing.T) {
	path := useConfig(t)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	edited := filepath.Join(dir, "gtp.cfg")
	if err := os.WriteFile(edited, []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := goban(t, "init", "--katago", "katago", "--model", "net.bin.gz"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if data, _ := os.ReadFile(edited); string(data) != "# mine\n" {
		t.Errorf("an existing base config was overwritten: %q", data)
	}
	data, err := os.ReadFile(filepath.Join(dir, "analysis.cfg"))
	if err != nil || !strings.Contains(string(data), "numSearchThreadsPerAnalysisThread = 4\n") {
		t.Errorf("analysis.cfg = %q, %v; want 4 threads by default", data, err)
	}
	// A relative model path is saved as an absolute one.
	if c := readConfig(t, path); !filepath.IsAbs(c.Model) {
		t.Errorf("model = %q; want an absolute path", c.Model)
	}
}

func TestInitRefusesBadSettings(t *testing.T) {
	path := useConfig(t)
	cases := map[string][]string{
		"nothing":          {"init"},
		"both modes":       {"init", "--command", "x", "--katago", "k", "--model", "m"},
		"native no model":  {"init", "--katago", "k"},
		"command no model": {"init", "--command", "x", "--model", "m"},
		"bad env":          {"init", "--command", "x", "--env", "NOEQUALS"},
		"bad threads":      {"init", "--katago", "k", "--model", "m", "--threads", "0"},
	}
	for name, args := range cases {
		if code, _, errOut := goban(t, args...); code != 2 || errOut == "" {
			t.Errorf("%s: exit %d, stderr %q; want exit 2 and a message", name, code, errOut)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a config file was written by a refused init: %v", err)
	}
}

func TestRunNeedsToken(t *testing.T) {
	path := useConfig(t)
	writeConfig(t, path, Config{Mode: "command", Command: "x"})
	code, _, errOut := goban(t, "run")
	if code != 1 || !strings.Contains(errOut, "goban-connector pair CODE") {
		t.Errorf("exit %d, stderr %q; want a message to pair first", code, errOut)
	}
}

func TestRunNeedsEngine(t *testing.T) {
	path := useConfig(t)
	writeConfig(t, path, Config{Token: "gct_x"})
	code, _, errOut := goban(t, "run")
	if code != 1 || !strings.Contains(errOut, "goban-connector init") {
		t.Errorf("exit %d, stderr %q; want a message to run init", code, errOut)
	}
}

func TestExitCodes(t *testing.T) {
	cases := []struct {
		err  error
		code int
		msg  string
	}{
		{nil, 0, ""},
		{context.Canceled, 0, ""},
		{client.ErrUnauthorized, 3, "The token is not valid any more. Pair again: goban-connector pair CODE"},
		{client.ErrReplaced, 4, "Another goban-connector with the same token connected. This one stops."},
		{errors.New("boom"), 1, "boom"},
	}
	for _, c := range cases {
		var errOut bytes.Buffer
		if got := exitCode(c.err, &errOut); got != c.code {
			t.Errorf("exitCode(%v) = %d, want %d", c.err, got, c.code)
		}
		if got := strings.TrimSpace(errOut.String()); got != c.msg {
			t.Errorf("exitCode(%v) printed %q, want %q", c.err, got, c.msg)
		}
	}
}

func TestRunUnauthorizedExits3(t *testing.T) {
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skip("there is no true program")
	}
	path := useConfig(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()
	writeConfig(t, path, Config{Server: srv.URL, Token: "gct_bad", Mode: "command", Command: truePath})
	code, _, errOut := goban(t, "run")
	if code != 3 || !strings.Contains(errOut, "Pair again: goban-connector pair CODE") {
		t.Errorf("exit %d, stderr %q", code, errOut)
	}
	if strings.Contains(errOut, "gct_bad") {
		t.Error("the token was printed")
	}
}

func TestVersionAndUsage(t *testing.T) {
	if code, out, _ := goban(t, "version"); code != 0 || strings.TrimSpace(out) != "dev" {
		t.Errorf("version: exit %d, stdout %q", code, out)
	}
	for _, args := range [][]string{{}, {"nope"}} {
		if code, _, errOut := goban(t, args...); code != 2 || !strings.Contains(errOut, "goban-connector pair CODE") {
			t.Errorf("%v: exit %d, stderr %q; want exit 2 and the usage", args, code, errOut)
		}
	}
	if code, out, _ := goban(t, "help"); code != 0 || !strings.Contains(out, "goban-connector run") {
		t.Errorf("help: exit %d, stdout %q", code, out)
	}
}
