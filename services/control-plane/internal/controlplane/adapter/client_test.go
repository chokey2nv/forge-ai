package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSendsTheSecretAndTheRunsToken(t *testing.T) {
	var auth string
	var got StartRunRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got)
		if auth != "Bearer s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, Token: "s3cret", CallbackToken: func(id string) string { return "tok-" + id }}
	if _, err := c.StartRun(context.Background(), &StartRunRequest{RunID: "run-a-rev1-0", CallbackRunID: "run-a"}); err != nil {
		t.Fatal(err)
	}
	if got.CallbackToken != "tok-run-a" {
		t.Errorf("a reviewer reports against the callback run: token %q", got.CallbackToken)
	}
	if _, err := c.StartRun(context.Background(), &StartRunRequest{RunID: "run-b"}); err != nil || got.CallbackToken != "tok-run-b" {
		t.Errorf("own run: %v %q", err, got.CallbackToken)
	}
	c.Token = "wrong"
	if _, err := c.StartRun(context.Background(), &StartRunRequest{RunID: "run-c"}); err == nil {
		t.Error("a refused secret must be an error")
	}
}
