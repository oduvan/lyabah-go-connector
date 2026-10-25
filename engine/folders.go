package engine

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/oduvan/lyabah-go-connector/protocol"
)

// Each user has two folders, like /players/<ID>/… on the Jetson:
// <DataDir>/players/<ID>/avoid-repeat (KataGo's own opening records) and
// <DataDir>/players/<ID>/sgf (the user's played games, as <GAME>.sgf).
// The ids are checked by package protocol before they get here, so they
// are plain digits and letters.

// maxSGF is the biggest SGF put-sgf accepts.
const maxSGF = 5242880

// Folder and file modes: the data belongs to the user running the connector.
const (
	dirMode  = 0o700
	fileMode = 0o600
)

// userDir is the folder of a user.
func (e *nativeEngine) userDir(user string) string {
	return filepath.Join(e.o.DataDir, "players", user)
}

// makeUserDirs creates the user's two folders.
func (e *nativeEngine) makeUserDirs(user string) error {
	for _, d := range []string{"avoid-repeat", "sgf"} {
		if err := os.MkdirAll(filepath.Join(e.userDir(user), d), dirMode); err != nil {
			return err
		}
	}
	return nil
}

// putSGF saves a game's SGF. It is written to a temp file first and then
// renamed, so KataGo never reads half a file.
func (e *nativeEngine) putSGF(user, game string, sgf []byte) error {
	if len(sgf) > maxSGF {
		return fmt.Errorf("the SGF is bigger than %d bytes", maxSGF)
	}
	if err := e.makeUserDirs(user); err != nil {
		return err
	}
	dir := filepath.Join(e.userDir(user), "sgf")
	tmp, err := os.CreateTemp(dir, "."+game+".*.part")
	if err != nil {
		return err
	}
	_, err = tmp.Write(sgf)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, game+".sgf"))
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}

// delSGF deletes a game's SGF. A missing one is not an error.
func (e *nativeEngine) delSGF(user, game string) error {
	err := os.Remove(filepath.Join(e.userDir(user), "sgf", game+".sgf"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// delUser deletes a user's folder.
func (e *nativeEngine) delUser(user string) error {
	return os.RemoveAll(e.userDir(user))
}

// syncUsers deletes the folders of users not in the list (user ids on
// stdin, one per line). It refuses an empty list. Only folders named like
// a user id are looked at; anything else stays.
func (e *nativeEngine) syncUsers(stdin []byte) ([]byte, error) {
	keep := map[string]bool{}
	for line := range strings.SplitSeq(string(stdin), "\n") {
		id := strings.Join(strings.Fields(line), "")
		if id == "" {
			continue
		}
		if !protocol.UserIDRE.MatchString(id) {
			return nil, fmt.Errorf("bad user ID '%s' (digits only)", id)
		}
		keep[id] = true
	}
	if len(keep) == 0 {
		return nil, errors.New("sync-users: refusing an empty list of users")
	}

	players := filepath.Join(e.o.DataDir, "players")
	entries, err := os.ReadDir(players)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	removed := 0
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || !protocol.UserIDRE.MatchString(id) || keep[id] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(players, id)); err != nil {
			return nil, err
		}
		removed++
	}
	return fmt.Appendf(nil, "removed %d folders\n", removed), nil
}

// listSGF prints the game ids of the user's SGF files, sorted, one per
// line.
func (e *nativeEngine) listSGF(user string) ([]byte, error) {
	entries, err := os.ReadDir(filepath.Join(e.userDir(user), "sgf"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var games []string
	for _, entry := range entries {
		game, ok := strings.CutSuffix(entry.Name(), ".sgf")
		if ok && entry.Type().IsRegular() && protocol.GameIDRE.MatchString(game) {
			games = append(games, game)
		}
	}
	sort.Strings(games)
	var out bytes.Buffer
	for _, g := range games {
		out.WriteString(g + "\n")
	}
	return out.Bytes(), nil
}
