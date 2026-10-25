package engine

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// The game's settings come in --config as "key = value" lines. They are
// checked line by line like the wrapper's check_config_lines, then merged
// into a base config like its merge_config. They are written to a file,
// never run.

var (
	// lineRE splits a line into its key and its value.
	lineRE = regexp.MustCompile(`^[[:space:]]*([^=[:space:]]+)[[:space:]]*=[[:space:]]*(.*)$`)
	keyRE  = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	// Values: letters, digits, space and _ . , : / + @ -  (no quotes, no #,
	// no shell characters). That covers numbers (1e50, -0.90), true/false,
	// profiles (rank_5k, proyear_1990), folder paths and seeds.
	valueRE = regexp.MustCompile(`^[A-Za-z0-9 _.,:/+@-]*$`)
	// baseKeyRE is a base config line that sets a key.
	baseKeyRE = regexp.MustCompile(`^[ \t]*[A-Za-z0-9_.-]+[ \t]*=`)
)

// The two opening folders. A gtp process may use its own user's folders
// only; the analysis process none.
const (
	avoidRepeatKey = "autoAvoidRepeatDir"
	sgfPatternKey  = "avoidSgfPatternDirs"
)

// errBothFolders is the wrapper's message for both opening folders at once.
var errBothFolders = errors.New("autoAvoidRepeatDir and avoidSgfPatternDirs cannot both be set: KataGo refuses to start with both")

// ruleKeys are KataGo's single rule keys. KataGo refuses them together
// with "rules".
var ruleKeys = []string{"koRule", "scoringRule", "taxRule", "multiStoneSuicideLegal", "hasButton", "whiteHandicapBonus", "friendlyPassOk"}

// CheckConfig checks the "key = value" lines of a --config and returns them
// cleaned up, one "key = value" per line. Blank lines and lines starting
// with # are left out. mode is "gtp" (the two opening folders must be the
// folders of user) or "analysis" (no folders at all).
func CheckConfig(text, mode string, user string) (string, error) {
	if mode != "gtp" && mode != "analysis" {
		return "", fmt.Errorf("unknown config mode %s", mode)
	}
	var out strings.Builder
	seen := map[string]bool{}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		n := i + 1
		bad := func(what string) error { return fmt.Errorf("config line %d: %s", n, what) }
		if strings.Contains(line, "\r") {
			return "", bad("carriage return")
		}
		trimmed := strings.TrimLeft(line, " \t\v\f")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		m := lineRE.FindStringSubmatch(line)
		if m == nil {
			return "", bad("not 'key = value'")
		}
		// Trailing spaces are not part of the value.
		key, value := m[1], strings.TrimRight(m[2], " \t\v\f")
		if !keyRE.MatchString(key) {
			return "", bad("bad key")
		}
		if !valueRE.MatchString(value) {
			return "", bad("bad value for " + key)
		}
		if seen[key] {
			return "", bad(key + " is given twice")
		}
		seen[key] = true

		switch {
		case key == avoidRepeatKey || key == sgfPatternKey:
			if mode != "gtp" {
				return "", bad(key + " is not allowed here")
			}
			want := "/players/" + user + "/sgf"
			if key == avoidRepeatKey {
				want = "/players/" + user + "/avoid-repeat"
			}
			if value != want {
				return "", bad(key + " must be " + want)
			}
		case serverLevelKey(key):
			// Server-level keys that touch files or logging stay in the
			// base config.
			return "", bad(key + " is a server-level setting")
		}
		fmt.Fprintf(&out, "%s = %s\n", key, value)
	}
	return out.String(), nil
}

// serverLevelKey says whether key is one a game may not set: log*,
// homeDataDir, *File, *FileName, *Dir, *Dirs, *Path, *Paths.
func serverLevelKey(key string) bool {
	if strings.HasPrefix(key, "log") || key == "homeDataDir" {
		return true
	}
	for _, end := range []string{"File", "FileName", "Dir", "Dirs", "Path", "Paths"} {
		if strings.HasSuffix(key, end) {
			return true
		}
	}
	return false
}

// MergeConfig returns base with the game's keys replacing its own. KataGo
// refuses a config that gives a key twice, so a base line whose key the
// game gives is left out. The game's single rule keys (koRule, ...) also
// drop the base "rules" line, because KataGo refuses both together. The
// game's lines come at the end, after a "from the app" line.
func MergeConfig(base, game string) string {
	gameLines := splitLines(game)
	gameKeys := map[string]bool{}
	for _, line := range gameLines {
		gameKeys[lineKey(line)] = true
	}
	for _, k := range ruleKeys {
		if gameKeys[k] {
			gameKeys["rules"] = true
		}
	}

	var out strings.Builder
	for _, line := range splitLines(base) {
		if baseKeyRE.MatchString(line) && gameKeys[lineKey(strings.TrimLeft(line, " \t"))] {
			continue
		}
		out.WriteString(line + "\n")
	}
	out.WriteString("\n# ---------------------------------------------------------------- from the app\n")
	for _, line := range gameLines {
		out.WriteString(line + "\n")
	}
	return out.String()
}

// lineKey is the part of a line before its first "=", without the spaces
// before the "=".
func lineKey(line string) string {
	if i := strings.Index(line, "="); i >= 0 {
		line = line[:i]
	}
	return strings.TrimRight(line, " \t")
}

// splitLines splits text into lines. A last line end does not start
// another line.
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}
