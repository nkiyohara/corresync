package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nkiyohara/corresync/internal/releasepublication"
)

func TestWaitForRegistryRetriesUntilExactRecord(t *testing.T) {
	t.Parallel()
	candidate := releasepublication.Candidate{Version: "1.2.3"}
	client := &sequenceClient{responses: []int{http.StatusNotFound, http.StatusNotFound}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := waitForRegistry(ctx, client, candidate, time.Millisecond); err == nil || client.calls < 2 {
		t.Fatal("registry timeout was not reported after retries")
	}
}

type sequenceClient struct {
	responses []int
	calls     int
}

func (client *sequenceClient) Do(_ *http.Request) (*http.Response, error) {
	client.calls++
	status := http.StatusNotFound
	if len(client.responses) > 0 {
		status = client.responses[0]
		client.responses = client.responses[1:]
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(strings.Repeat("x", 8))),
	}, nil
}
