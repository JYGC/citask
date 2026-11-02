package store

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHeartbeatIsABareGetToTheConfiguredUrl(t *testing.T) {
	var receivedMethod, receivedPath, receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		receivedMethod, receivedPath, receivedBody = request.Method, request.URL.Path, string(body)
	}))
	t.Cleanup(server.Close)

	err := HeartbeatStore{Url: server.URL + "/check-id", RequestTimeLimit: 10 * time.Second}.SendHeartbeat(context.Background())

	if err != nil {
		t.Fatal(err)
	}
	if receivedMethod != http.MethodGet || receivedPath != "/check-id" || receivedBody != "" {
		t.Errorf("got %s %s with body %q", receivedMethod, receivedPath, receivedBody)
	}
}

func TestHeartbeatWithNoUrlMakesNoRequest(t *testing.T) {
	if err := (HeartbeatStore{RequestTimeLimit: 10 * time.Second}).SendHeartbeat(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatAnsweredWithAnErrorStatusIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "stub outage", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	err := HeartbeatStore{Url: server.URL, RequestTimeLimit: 10 * time.Second}.SendHeartbeat(context.Background())

	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected the status to be named, got %v", err)
	}
}

func TestHeartbeatGivesUpAtItsTimeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)

	startedAt := time.Now()
	err := HeartbeatStore{Url: server.URL, RequestTimeLimit: 200 * time.Millisecond}.SendHeartbeat(context.Background())

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the time limit to be reached, got %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 5*time.Second {
		t.Errorf("giving up took %s", elapsed)
	}
}
