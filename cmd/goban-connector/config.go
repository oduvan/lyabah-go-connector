package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/oduvan/lyabah-go-connector/client"
)

// configEnv is the environment variable that names the config file.
const configEnv = "GOBAN_CONNECTOR_CONFIG"

// defaultMaxGames is how many games may play at once when the config does
// not say.
const defaultMaxGames = 2

// Config is the config file. A field that is empty or 0 takes its default
// when the connector uses it: see the methods below.
type Config struct {
	Server      string `json:"server,omitempty"`
	Token       string `json:"token,omitempty"`
	ConnectorID int64  `json:"connectorId,omitempty"`
	Name        string `json:"name,omitempty"`

	// Mode is "command" or "native".
	Mode    string   `json:"mode,omitempty"`
	Command string   `json:"command,omitempty"`
	Env     []string `json:"env,omitempty"`

	Katago         string `json:"katago,omitempty"`
	Model          string `json:"model,omitempty"`
	HumanModel     string `json:"humanModel,omitempty"`
	GTPConfig      string `json:"gtpConfig,omitempty"`
	AnalysisConfig string `json:"analysisConfig,omitempty"`
	DataDir        string `json:"dataDir,omitempty"`

	MaxGames int `json:"maxGames,omitempty"`
}

// configPath is the config file's path: $GOBAN_CONNECTOR_CONFIG, else
// goban-connector/config.json in the user's config folder.
func configPath() (string, error) {
	if p := os.Getenv(configEnv); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("could not find the config folder (set %s): %w", configEnv, err)
	}
	return filepath.Join(dir, "goban-connector", "config.json"), nil
}

// loadConfig reads the config file. A file that is not there gives an empty
// config.
func loadConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, fmt.Errorf("the config file %s is not valid JSON: %w", path, err)
	}
	return c, nil
}

// saveConfig writes the config file with mode 0600 (its folder, when it is
// new, with 0700). It writes a new file and moves it into place, so that a
// crash never leaves half a file.
func saveConfig(path string, c Config) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// server is Goban's address.
func (c Config) server() string {
	if c.Server == "" {
		return client.DefaultServer
	}
	return c.Server
}

// dataDir is the folder for the players' folders and the running configs:
// the config file's folder plus data, unless the config says otherwise.
func (c Config) dataDir(configFile string) string {
	if c.DataDir == "" {
		return filepath.Join(filepath.Dir(configFile), "data")
	}
	return c.DataDir
}

// maxGames is how many games may play at once.
func (c Config) maxGames() int {
	if c.MaxGames <= 0 {
		return defaultMaxGames
	}
	return c.MaxGames
}

// absPath makes a file path absolute, so that it still works when the
// connector runs from another folder (for example as a service). A bare
// program name, which is looked up in PATH, stays as it is when
// keepBareName is true.
func absPath(p string, keepBareName bool) (string, error) {
	if p == "" || (keepBareName && !strings.ContainsAny(p, `/\`)) {
		return p, nil
	}
	return filepath.Abs(p)
}

// baseConfig is the text of a default base config for KataGo: the example
// settings of KataGo with the settings Goban needs. kind is "gtp" or
// "analysis". Goban adds the settings of each game on top of it.
func baseConfig(kind string, threads int) string {
	var b strings.Builder
	if kind == "gtp" {
		b.WriteString("# Base settings for KataGo's GTP games.\n")
	} else {
		b.WriteString("# Base settings for KataGo's analysis.\n")
	}
	b.WriteString("# Written by goban-connector init. You may change it.\n")
	b.WriteString("# Goban adds the settings of each game after these lines.\n\n")
	b.WriteString("logToStderr = true\n")
	b.WriteString("logAllGTPCommunication = false\n")
	b.WriteString("logSearchInfo = false\n")
	b.WriteString("ponderingEnabled = false\n")
	b.WriteString("reportAnalysisWinratesAs = BLACK\n")
	if kind == "gtp" {
		b.WriteString("rules = tromp-taylor\n")
		fmt.Fprintf(&b, "numSearchThreads = %d\n", threads)
	} else {
		b.WriteString("numAnalysisThreads = 2\n")
		fmt.Fprintf(&b, "numSearchThreadsPerAnalysisThread = %d\n", threads)
	}
	b.WriteString("nnCacheSizePowerOfTwo = 20\n")
	b.WriteString("nnMutexPoolSizePowerOfTwo = 16\n")
	return b.String()
}
