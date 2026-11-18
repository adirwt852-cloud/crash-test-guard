package protocol

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func newClient(srv *httptest.Server) *Client {
	return NewClient(ClientConfig{
		BaseURL:        srv.URL,
		ConnectTimeout: time.Second,
		RequestTimeout: 2 * time.Second,
		MaxRetries:     0,
		BaseRetryDelay:  time.Millisecond,
		MaxRetryDelay:  10 * time.Millisecond,
	})
}

func TestNewClientWithConfig(t *testing.T) {
	cfg := ClientConfig{BaseURL: "http://example.com"}
	c := NewClient(cfg)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
	if c.config.BaseURL != "http://example.com" {
		t.Errorf("unexpected base url: %s", c.config.BaseURL)
	}
	if c.config.RequestTimeout != 30*time.Second {
		t.Errorf("expected default request timeout, got %v", c.config.RequestTimeout)
	}
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/health" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(HealthResponse{
			ProtocolVersion: ProtocolVersion,
			AgentVersion:    "1.2.3",
			Status:          StatusHealthy,
		})
	})
	c := newClient(srv)
	resp, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health error: %v", err)
	}
	if resp.Status != StatusHealthy {
		t.Errorf("unexpected status: %s", resp.Status)
	}
}

func TestStartWorkflow(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/workflow/start" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(StartResponse{WorkflowID: "wf-1", Status: StatusRunning})
	})
	c := newClient(srv)
	resp, err := c.StartWorkflow(context.Background(), map[string]any{"x": 1})
	if err != nil {
		t.Fatalf("StartWorkflow error: %v", err)
	}
	if resp.WorkflowID != "wf-1" {
		t.Errorf("unexpected workflow id: %s", resp.WorkflowID)
	}
}

func TestGetState(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workflow/wf-1/state" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(StateResponse{WorkflowID: "wf-1", Status: StatusRunning, CurrentStep: "s1"})
	})
	c := newClient(srv)
	resp, err := c.GetState(context.Background(), "wf-1")
	if err != nil {
		t.Fatalf("GetState error: %v", err)
	}
	if resp.CurrentStep != "s1" {
		t.Errorf("unexpected step: %s", resp.CurrentStep)
	}
}

func TestGetCheckpoint(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workflow/wf-1/checkpoint" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(CheckpointEnvelope{
			ProtocolVersion: ProtocolVersion,
			WorkflowID:      "wf-1",
			CheckpointID:    "cp-1",
			Sequence:        5,
		})
	})
	c := newClient(srv)
	resp, err := c.GetCheckpoint(context.Background(), "wf-1")
	if err != nil {
		t.Fatalf("GetCheckpoint error: %v", err)
	}
	if resp.CheckpointID != "cp-1" {
		t.Errorf("unexpected checkpoint id: %s", resp.CheckpointID)
	}
}

func TestGetEvents(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "since=3" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		json.NewEncoder(w).Encode([]Event{{Sequence: 4, Type: "step", StepID: "s"}})
	})
	c := newClient(srv)
	events, err := c.GetEvents(context.Background(), "wf-1", 3)
	if err != nil {
		t.Fatalf("GetEvents error: %v", err)
	}
	if len(events) != 1 || events[0].Sequence != 4 {
		t.Errorf("unexpected events: %+v", events)
	}
}

func TestResumeWorkflow(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/workflow/wf-1/resume" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(ResumeResponse{Status: StatusResumed, WorkflowID: "wf-1"})
	})
	c := newClient(srv)
	resp, err := c.ResumeWorkflow(context.Background(), "wf-1", "ckpt")
	if err != nil {
		t.Fatalf("ResumeWorkflow error: %v", err)
	}
	if resp.Status != StatusResumed {
		t.Errorf("unexpected status: %s", resp.Status)
	}
}

func TestErrorHandlingProblemDetails(t *testing.T) {
	for _, code := range []int{400, 404, 500} {
		code := code
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
				json.NewEncoder(w).Encode(ProblemDetails{Type: "err", Title: "Bad", Status: code, Detail: "detail"})
			})
			c := newClient(srv)
			_, err := c.Health(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			pd, ok := err.(*ProblemDetails)
			if !ok {
				t.Fatalf("expected ProblemDetails, got %T", err)
			}
			if pd.Status != code {
				t.Errorf("expected status %d, got %d", code, pd.Status)
			}
		})
	}
}

func TestRetryLogic(t *testing.T) {
	var calls int
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("oops"))
			return
		}
		json.NewEncoder(w).Encode(HealthResponse{ProtocolVersion: ProtocolVersion, Status: StatusHealthy})
	})
	c := NewClient(ClientConfig{
		BaseURL:        srv.URL,
		ConnectTimeout: time.Second,
		RequestTimeout: 2 * time.Second,
		MaxRetries:     3,
		BaseRetryDelay: time.Millisecond,
		MaxRetryDelay:  10 * time.Millisecond,
	})
	_, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health error: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 calls, got %d", calls)
	}
}

func TestTimeoutHandling(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		json.NewEncoder(w).Encode(HealthResponse{ProtocolVersion: ProtocolVersion, Status: StatusHealthy})
	})
	c := NewClient(ClientConfig{
		BaseURL:        srv.URL,
		ConnectTimeout: time.Second,
		RequestTimeout: 50 * time.Millisecond,
		MaxRetries:     0,
		BaseRetryDelay: time.Millisecond,
		MaxRetryDelay:  10 * time.Millisecond,
	})
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
