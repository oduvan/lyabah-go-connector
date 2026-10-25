package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPair(t *testing.T) {
	var got map[string]string
	var userAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/connector/pair" {
			http.NotFound(w, r)
			return
		}
		userAgent = r.Header.Get("User-Agent")
		got = nil
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch got["code"] {
		case "ABCD2345":
			w.Write([]byte(`{"token":"gct_new","connectorId":5,"name":"Desk"}`))
		case "BROKEN22":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"database is down"}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"This pairing code is not valid or has expired."}`))
		}
	}))
	defer srv.Close()
	info := PairInfo{Hostname: "desk", OS: "linux", Arch: "amd64", Version: "1.2.3"}
	ctx := context.Background()

	// The code is sent in upper case, without - and spaces.
	res, err := Pair(ctx, srv.URL+"/", " abcd-2345 ", info)
	if err != nil {
		t.Fatal(err)
	}
	if res != (PairResult{Token: "gct_new", ConnectorID: 5, Name: "Desk"}) {
		t.Errorf("Pair = %+v", res)
	}
	want := map[string]string{"code": "ABCD2345", "hostname": "desk", "os": "linux", "arch": "amd64", "version": "1.2.3"}
	if len(got) != len(want) {
		t.Errorf("request = %q; want %q", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("request %s = %q; want %q", k, got[k], v)
		}
	}
	if userAgent != "goban-connector/1.2.3" {
		t.Errorf("User-Agent = %q", userAgent)
	}

	// A refused code gives the server's text.
	_, err = Pair(ctx, srv.URL, "WXYZ-2345", info)
	if err == nil || err.Error() != "This pairing code is not valid or has expired." {
		t.Errorf("Pair with a wrong code = %v", err)
	}

	// A server error says so.
	_, err = Pair(ctx, srv.URL, "BROKEN22", info)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("Pair with a server error = %v", err)
	}

	// A server address that is not http or https.
	if _, err := Pair(ctx, "go.lyabah.com", "ABCD2345", info); err == nil {
		t.Error("Pair with a server without a scheme worked")
	}
}
