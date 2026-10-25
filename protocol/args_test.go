package protocol

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestCheckProcessArgs(t *testing.T) {
	cfg := base64.StdEncoding.EncodeToString([]byte("maxVisits = 10\n"))
	good := [][]string{
		{"gtp", "game-k7Qm2xPa9T", "--user", "7", "--no-human", "--config", cfg},
		{"gtp", "game-k7Qm2xPa9T", "--user", "7"},
		{"gtp", "game-x", "--config", cfg, "--user", "7"},
		{"gtp", "g", "--user", "9223372036854775807"},
		{"analysis", "analysis"},
		{"analysis", "analysis", "--config", cfg},
		{"analysis", "analysis", "--config", ""},
		{"analysis", "analysis", "--config", strings.Repeat("A", maxConfig)},
	}
	for _, args := range good {
		if err := CheckProcessArgs(args); err != nil {
			t.Errorf("CheckProcessArgs(%q) = %v; want nil", args, err)
		}
	}

	bad := [][]string{
		nil,
		{},
		{"gtp"},
		{"cleanup"},
		{"rm"},
		{"check-keys"},
		{"version"},
		{"put-sgf", "7", "k7Qm2xPa9T"},
		{"list-sgf", "7"},
		{"gtp", "game-k7Qm2xPa9T"},
		{"gtp", "game'x", "--user", "7"},
		{"gtp", "-game", "--user", "7"},
		{"gtp", "", "--user", "7"},
		{"gtp", strings.Repeat("a", 64), "--user", "7"},
		{"gtp", "game-x", "--user", "7", "--user", "8"},
		{"gtp", "game-x", "--user"},
		{"gtp", "game-x", "--user", "07"},
		{"gtp", "game-x", "--user", "7", "--config"},
		{"gtp", "game-x", "--user", "7", "--config", "a b"},
		{"gtp", "game-x", "--user", "7", "--config", "abc"},
		{"gtp", "game-x", "--user", "7", "--config", "maxVisits = 10"},
		{"gtp", "game-x", "--user", "7", "--human"},
		{"gtp", "game-x", "--user", "7", "extra"},
		{"analysis", "analysis", "--user", "7"},
		{"analysis", "analysis", "--no-human"},
		{"analysis", "analysis", "--config", strings.Repeat("A", maxConfig+4)},
		{"analysis"},
	}
	for _, args := range bad {
		if err := CheckProcessArgs(args); !errors.Is(err, ErrBadCommand) {
			t.Errorf("CheckProcessArgs(%q) = %v; want ErrBadCommand", args, err)
		}
	}
}

func TestCheckShortArgs(t *testing.T) {
	good := [][]string{
		{"version"},
		{"sync-users"},
		{"put-sgf", "7", "k7Qm2xPa9T"},
		{"del-sgf", "7", "k7Qm2xPa9T"},
		{"del-user", "12"},
		{"list-sgf", "7"},
	}
	for _, args := range good {
		if err := CheckShortArgs(args); err != nil {
			t.Errorf("CheckShortArgs(%q) = %v; want nil", args, err)
		}
	}

	bad := [][]string{
		nil,
		{},
		{"rm"},
		{"cleanup"},
		{"check-keys"},
		{"gtp", "game-k7Qm2xPa9T", "--user", "7"},
		{"analysis", "analysis"},
		{"version", "now"},
		{"sync-users", "7"},
		{"put-sgf"},
		{"put-sgf", "7"},
		{"put-sgf", "07", "k7Qm2xPa9T"},
		{"put-sgf", "7", "k7Qm2xPa9"},
		{"put-sgf", "7", "k7Qm2xPa9T", "extra"},
		{"del-sgf", "7", "k7Qm2x;a9T"},
		{"del-user"},
		{"del-user", "7 "},
		{"del-user", "-1"},
		{"del-user", "0"},
		{"del-user", "12", "13"},
		{"list-sgf"},
		{"list-sgf", "07"},
		{"list-sgf", "7", "8"},
		{"list-sgf", "7;id"},
	}
	for _, args := range bad {
		if err := CheckShortArgs(args); !errors.Is(err, ErrBadCommand) {
			t.Errorf("CheckShortArgs(%q) = %v; want ErrBadCommand", args, err)
		}
	}
}

func TestPatterns(t *testing.T) {
	if !UserIDRE.MatchString("7") || UserIDRE.MatchString("0") || UserIDRE.MatchString("07") {
		t.Error("UserIDRE is wrong")
	}
	if !GameIDRE.MatchString("k7Qm2xPa9T") || GameIDRE.MatchString("k7Qm2xPa9") {
		t.Error("GameIDRE is wrong")
	}
	if !NameRE.MatchString("game-k7Qm2xPa9T") || NameRE.MatchString("-game") {
		t.Error("NameRE is wrong")
	}
}
