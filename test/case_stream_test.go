//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bpradana/sandboxd/internal/presentation"
)

func (s *e2eSuite) runStreamCase(t *testing.T) {
	sandbox := s.createSandbox(t, presentation.CreateSandboxRequest{})

	body, _ := json.Marshal(presentation.ExecRequest{Command: "echo one; sleep 1; echo two >&2; exit 4", TimeoutSeconds: 10})
	req, _ := http.NewRequest(http.MethodPost, s.baseURL+"/api/v1/sandboxes/"+sandbox.ID+"/exec/stream", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}
	res, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type = %q", res.Header.Get("Content-Type"))
	}

	type event struct {
		name    string
		payload presentation.ExecStreamEvent
		at      time.Time
	}
	var events []event
	scanner := bufio.NewScanner(res.Body)
	var name string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var payload presentation.ExecStreamEvent
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
				t.Fatal(err)
			}
			events = append(events, event{name: name, payload: payload, at: time.Now()})
		}
	}

	if len(events) != 3 || events[0].name != "stdout" || events[1].name != "stderr" || events[2].name != "exit" {
		t.Fatalf("events = %+v", events)
	}
	if events[0].payload.Data != "one\n" || events[1].payload.Data != "two\n" || events[2].payload.Result.ExitCode != 4 {
		t.Fatalf("events = %+v", events)
	}
	if events[1].at.Sub(events[0].at) < 500*time.Millisecond {
		t.Fatal("output was buffered instead of streamed")
	}
}
