package application_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type heartbeatStubBehaviour int

const (
	acceptHeartbeats heartbeatStubBehaviour = iota
	failEveryHeartbeat
	neverAnswerHeartbeats
)

type heartbeatStub struct {
	server                        *httptest.Server
	mutex                         sync.Mutex
	behaviour                     heartbeatStubBehaviour
	requests                      []recordedRequest
	gitHubRequestsAtEachHeartbeat []int
}

func newHeartbeatStub(t *testing.T, github *gitHubStub) *heartbeatStub {
	t.Helper()
	stub := &heartbeatStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		gitHubRequestsSoFar := github.requestCount()
		stub.mutex.Lock()
		behaviour := stub.behaviour
		stub.requests = append(stub.requests, recordedRequest{
			Method: request.Method,
			Url:    request.URL.String(),
			Header: request.Header.Clone(),
			Body:   string(body),
		})
		stub.gitHubRequestsAtEachHeartbeat = append(stub.gitHubRequestsAtEachHeartbeat, gitHubRequestsSoFar)
		stub.mutex.Unlock()

		switch behaviour {
		case neverAnswerHeartbeats:
			// Returning when the client gives up keeps server.Close from waiting forever on this request.
			<-request.Context().Done()
		case failEveryHeartbeat:
			http.Error(writer, "stub outage", http.StatusBadGateway)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (stub *heartbeatStub) setBehaviour(behaviour heartbeatStubBehaviour) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.behaviour = behaviour
}

func (stub *heartbeatStub) count() int {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return len(stub.requests)
}

func (stub *heartbeatStub) lastRequest() recordedRequest {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return stub.requests[len(stub.requests)-1]
}

func (stub *heartbeatStub) gitHubRequestsAtLastHeartbeat() int {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	return stub.gitHubRequestsAtEachHeartbeat[len(stub.gitHubRequestsAtEachHeartbeat)-1]
}
