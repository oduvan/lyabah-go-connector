package protocol

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
)

// The connector runs only the commands of the wrapper (jetson/goban-katago
// in the Goban repository), with the wrapper's argument patterns. These are
// the same rules as the server checks before it sends a command, so a wrong
// command is stopped on both sides. No free text from the server reaches
// a shell or a file name.

// The patterns of the wrapper's arguments.
var (
	// NameRE is the name of a process: a game's name or "analysis".
	NameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)
	// UserIDRE is a user id: digits, no leading zero.
	UserIDRE = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	// GameIDRE is a game id: ten letters and digits.
	GameIDRE = regexp.MustCompile(`^[A-Za-z0-9]{10}$`)

	base64RE = regexp.MustCompile(`^[A-Za-z0-9+/]*={0,2}$`)
)

// maxConfig is the longest --config the wrapper accepts.
const maxConfig = 65536

// ErrBadCommand is returned for a command or an argument that does not
// follow the wrapper's grammar. Nothing is run then.
var ErrBadCommand = errors.New("not a valid wrapper command")

// CheckProcessArgs checks the args of a start message. The first one is the
// command, which must be gtp or analysis.
func CheckProcessArgs(args []string) error {
	if len(args) == 0 {
		return ErrBadCommand
	}
	switch args[0] {
	case "gtp", "analysis":
		return checkArgs(args[0], args[1:])
	}
	return ErrBadCommand
}

// CheckShortArgs checks the args of a run message. The first one is the
// command, which must be put-sgf, del-sgf, del-user, sync-users, list-sgf
// or version. (cleanup is not here: the connector runs it by itself.)
func CheckShortArgs(args []string) error {
	if len(args) == 0 {
		return ErrBadCommand
	}
	switch args[0] {
	case "put-sgf", "del-sgf", "del-user", "sync-users", "list-sgf", "version":
		return checkArgs(args[0], args[1:])
	}
	return ErrBadCommand
}

// checkArgs checks the arguments of one wrapper command.
func checkArgs(cmd string, args []string) error {
	bad := func(what string) error { return fmt.Errorf("%w: %s %s", ErrBadCommand, cmd, what) }
	switch cmd {
	case "version", "sync-users":
		if len(args) != 0 {
			return bad("takes no arguments")
		}
	case "put-sgf", "del-sgf":
		if len(args) != 2 || !UserIDRE.MatchString(args[0]) || !GameIDRE.MatchString(args[1]) {
			return bad("needs a user id and a game id")
		}
	case "del-user", "list-sgf":
		if len(args) != 1 || !UserIDRE.MatchString(args[0]) {
			return bad("needs a user id")
		}
	case "gtp", "analysis":
		if len(args) == 0 || !NameRE.MatchString(args[0]) {
			return bad("needs a name")
		}
		seen := map[string]bool{}
		for i := 1; i < len(args); i++ {
			opt := args[i]
			if seen[opt] {
				return bad("gives " + opt + " twice")
			}
			seen[opt] = true
			switch {
			case opt == "--user" && cmd == "gtp":
				if i+1 >= len(args) || !UserIDRE.MatchString(args[i+1]) {
					return bad("--user needs a user id")
				}
				i++
			case opt == "--no-human" && cmd == "gtp":
			case opt == "--config":
				if i+1 >= len(args) || !validConfig(args[i+1]) {
					return bad("--config needs base64")
				}
				i++
			default:
				return bad("has an unknown option")
			}
		}
		if cmd == "gtp" && !seen["--user"] {
			return bad("needs --user")
		}
	default:
		return ErrBadCommand
	}
	return nil
}

// validConfig says whether s is standard base64 the wrapper accepts.
func validConfig(s string) bool {
	if len(s) > maxConfig || !base64RE.MatchString(s) {
		return false
	}
	_, err := base64.StdEncoding.DecodeString(s)
	return err == nil
}
