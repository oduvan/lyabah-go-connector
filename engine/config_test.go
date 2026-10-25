package engine

import (
	"strings"
	"testing"
)

func TestCheckConfig(t *testing.T) {
	good := []struct {
		text, mode, user, want string
	}{
		{"maxVisits = 10\n", "gtp", "7", "maxVisits = 10\n"},
		{"  maxVisits=10   \n\n# a comment\n   \t\nresignThreshold = -0.90", "gtp", "7",
			"maxVisits = 10\nresignThreshold = -0.90\n"},
		{"humanSLProfile = rank_5k\nchosenMoveTemperature = 0.5\n", "analysis", "",
			"humanSLProfile = rank_5k\nchosenMoveTemperature = 0.5\n"},
		{"autoAvoidRepeatDir = /players/7/avoid-repeat\n", "gtp", "7",
			"autoAvoidRepeatDir = /players/7/avoid-repeat\n"},
		{"avoidSgfPatternDirs = /players/7/sgf\n", "gtp", "7",
			"avoidSgfPatternDirs = /players/7/sgf\n"},
		{"", "gtp", "7", ""},
		{"empty =\n", "gtp", "7", "empty = \n"},
	}
	for _, c := range good {
		got, err := CheckConfig(c.text, c.mode, c.user)
		if err != nil || got != c.want {
			t.Errorf("CheckConfig(%q, %s) = %q, %v; want %q", c.text, c.mode, got, err, c.want)
		}
	}

	bad := []struct {
		text, mode, user, want string
	}{
		{"maxVisits = 10\r\n", "gtp", "7", "config line 1: carriage return"},
		{"maxVisits = 10\nmaxVisits\n", "gtp", "7", "config line 2: not 'key = value'"},
		{"= 10\n", "gtp", "7", "config line 1: not 'key = value'"},
		{"max$Visits = 10\n", "gtp", "7", "config line 1: bad key"},
		{"maxVisits = 10;rm\n", "gtp", "7", "config line 1: bad value for maxVisits"},
		{"maxVisits = \"10\"\n", "gtp", "7", "config line 1: bad value for maxVisits"},
		{"maxVisits = a=b\n", "gtp", "7", "config line 1: bad value for maxVisits"},
		{"maxVisits = 10\nmaxVisits = 20\n", "gtp", "7", "config line 2: maxVisits is given twice"},
		{"autoAvoidRepeatDir = /players/8/avoid-repeat\n", "gtp", "7",
			"config line 1: autoAvoidRepeatDir must be /players/7/avoid-repeat"},
		{"autoAvoidRepeatDir = /players/7/sgf\n", "gtp", "7",
			"config line 1: autoAvoidRepeatDir must be /players/7/avoid-repeat"},
		{"avoidSgfPatternDirs = /players/7/../8/sgf\n", "gtp", "7",
			"config line 1: avoidSgfPatternDirs must be /players/7/sgf"},
		{"autoAvoidRepeatDir = /players/7/avoid-repeat\n", "analysis", "",
			"config line 1: autoAvoidRepeatDir is not allowed here"},
		{"avoidSgfPatternDirs = /players/7/sgf\n", "analysis", "",
			"config line 1: avoidSgfPatternDirs is not allowed here"},
		{"logDir = /tmp\n", "gtp", "7", "config line 1: logDir is a server-level setting"},
		{"logToStderr = false\n", "gtp", "7", "config line 1: logToStderr is a server-level setting"},
		{"homeDataDir = /tmp\n", "gtp", "7", "config line 1: homeDataDir is a server-level setting"},
		{"modelFile = /etc/passwd\n", "gtp", "7", "config line 1: modelFile is a server-level setting"},
		{"logFile = /etc/passwd\n", "analysis", "", "config line 1: logFile is a server-level setting"},
		{"foo.Path = /etc\n", "gtp", "7", "config line 1: foo.Path is a server-level setting"},
		{"trtCacheFileName = x\n", "gtp", "7", "config line 1: trtCacheFileName is a server-level setting"},
		{"fooDirs = /etc\n", "gtp", "7", "config line 1: fooDirs is a server-level setting"},
		{"fooPaths = /etc\n", "gtp", "7", "config line 1: fooPaths is a server-level setting"},
		{"maxVisits = 10\n", "check-keys", "", "unknown config mode check-keys"},
	}
	for _, c := range bad {
		got, err := CheckConfig(c.text, c.mode, c.user)
		if err == nil || err.Error() != c.want {
			t.Errorf("CheckConfig(%q, %s) = %q, %v; want error %q", c.text, c.mode, got, err, c.want)
		}
	}
}

func TestMergeConfig(t *testing.T) {
	base := strings.Join([]string{
		"# base",
		"logToStderr = true",
		"  maxVisits = 100",
		"rules = tromp-taylor",
		"numSearchThreads = 16",
		"",
	}, "\n")

	// A game key replaces the base key; the rest stays in its place.
	got := MergeConfig(base, "maxVisits = 10\n")
	want := strings.Join([]string{
		"# base",
		"logToStderr = true",
		"rules = tromp-taylor",
		"numSearchThreads = 16",
		"",
		"# ---------------------------------------------------------------- from the app",
		"maxVisits = 10",
		"",
	}, "\n")
	if got != want {
		t.Errorf("MergeConfig =\n%s\nwant\n%s", got, want)
	}

	// A single rule key drops the base "rules" line.
	got = MergeConfig(base, "koRule = SIMPLE\n")
	want = strings.Join([]string{
		"# base",
		"logToStderr = true",
		"  maxVisits = 100",
		"numSearchThreads = 16",
		"",
		"# ---------------------------------------------------------------- from the app",
		"koRule = SIMPLE",
		"",
	}, "\n")
	if got != want {
		t.Errorf("MergeConfig =\n%s\nwant\n%s", got, want)
	}

	// No game lines: the base and the empty block from the app.
	got = MergeConfig("a = 1", "")
	want = "a = 1\n\n# ---------------------------------------------------------------- from the app\n"
	if got != want {
		t.Errorf("MergeConfig = %q; want %q", got, want)
	}
}
