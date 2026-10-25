package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// PairInfo is what the connector tells Goban about this computer when it
// pairs.
type PairInfo struct {
	Hostname string
	OS       string
	Arch     string
	Version  string // the connector's version
}

// PairResult is Goban's answer to a pairing code that works.
type PairResult struct {
	Token       string `json:"token"`
	ConnectorID int64  `json:"connectorId"`
	Name        string `json:"name"`
}

// pairTimeout bounds the pairing request.
const pairTimeout = 30 * time.Second

// Pair trades a pairing code for a connector token. The code may be typed
// in any case, with or without the dash and spaces. When Goban refuses the
// code, the error is Goban's own text, like "This pairing code is not valid
// or has expired.". An empty server means DefaultServer.
func Pair(ctx context.Context, server, code string, info PairInfo) (PairResult, error) {
	var res PairResult
	if server == "" {
		server = DefaultServer
	}
	pairURL, err := serverURL(server, "/api/connector/pair", false)
	if err != nil {
		return res, err
	}
	body, err := json.Marshal(map[string]string{
		"code":     normalizeCode(code),
		"hostname": info.Hostname,
		"os":       info.OS,
		"arch":     info.Arch,
		"version":  info.Version,
	})
	if err != nil {
		return res, err
	}

	ctx, cancel := context.WithTimeout(ctx, pairTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pairURL, bytes.NewReader(body))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent(info.Version))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return res, err
	}

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &e) == nil && e.Error != "" {
			return res, errors.New(e.Error)
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return res, fmt.Errorf("Goban answered %s", resp.Status)
	}
	if err := json.Unmarshal(data, &res); err != nil || res.Token == "" {
		return PairResult{}, errors.New("Goban's answer has no connector token")
	}
	return res, nil
}

// normalizeCode writes a pairing code the way Goban keeps it: upper case,
// without dashes and spaces.
func normalizeCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, strings.ToUpper(code))
}
