package store

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

type HeartbeatStore struct {
	Url              string
	RequestTimeLimit time.Duration
}

func (heartbeatStore HeartbeatStore) SendHeartbeat(ctx context.Context) error {
	if heartbeatStore.Url == "" {
		return nil
	}
	requestContext, cancel := context.WithTimeout(ctx, heartbeatStore.RequestTimeLimit)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, heartbeatStore.Url, nil)
	if err != nil {
		return fmt.Errorf("build heartbeat request: %w", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("send heartbeat: %w", err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("heartbeat answered %d", response.StatusCode)
	}
	return nil
}
