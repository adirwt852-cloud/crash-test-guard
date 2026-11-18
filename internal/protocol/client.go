package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type ClientConfig struct {
	BaseURL        string
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
	MaxRetries     int
	BaseRetryDelay time.Duration
	MaxRetryDelay  time.Duration
}

type Client struct {
	config     ClientConfig
	httpClient *http.Client
}

func NewClient(config ClientConfig) *Client {
	if config.ConnectTimeout == 0 {
		config.ConnectTimeout = 10 * time.Second
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 30 * time.Second
	}
	if config.MaxRetries == 0 {
		config.MaxRetries = 3
	}
	if config.BaseRetryDelay == 0 {
		config.BaseRetryDelay = 100 * time.Millisecond
	}
	if config.MaxRetryDelay == 0 {
		config.MaxRetryDelay = 5 * time.Second
	}

	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: config.ConnectTimeout,
		}).DialContext,
		TLSHandshakeTimeout: config.ConnectTimeout,
	}

	return &Client{
		config: config,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   config.RequestTimeout,
		},
	}
}

func (c *Client) doRequest(ctx context.Context, method, path string, body any, result any) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	url := c.config.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	var lastErr error
	delay := c.config.BaseRetryDelay

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}

			delay *= 2
			if delay > c.config.MaxRetryDelay {
				delay = c.config.MaxRetryDelay
			}
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("request failed: %w", err)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("read response: %w", err)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if result != nil && len(respBody) > 0 {
				if err := json.Unmarshal(respBody, result); err != nil {
					return fmt.Errorf("unmarshal response: %w", err)
				}
			}
			return nil
		}

		var prob ProblemDetails
		if err := json.Unmarshal(respBody, &prob); err == nil && prob.Status > 0 {
			return &prob
		}

		lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return lastErr
}

func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	var resp HealthResponse
	if err := c.doRequest(ctx, http.MethodGet, "/health", nil, &resp); err != nil {
		return nil, err
	}

	if resp.ProtocolVersion != ProtocolVersion {
		return nil, fmt.Errorf("protocol version mismatch: expected %s, got %s", ProtocolVersion, resp.ProtocolVersion)
	}

	return &resp, nil
}

func (c *Client) StartWorkflow(ctx context.Context, input map[string]any) (*StartResponse, error) {
	var resp StartResponse
	req := StartRequest{Input: input}
	if err := c.doRequest(ctx, http.MethodPost, "/workflow/start", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetState(ctx context.Context, workflowID string) (*StateResponse, error) {
	var resp StateResponse
	path := fmt.Sprintf("/workflow/%s/state", workflowID)
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetCheckpoint(ctx context.Context, workflowID string) (*CheckpointEnvelope, error) {
	var resp CheckpointEnvelope
	path := fmt.Sprintf("/workflow/%s/checkpoint", workflowID)
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) GetEvents(ctx context.Context, workflowID string, sinceSeq int64) ([]Event, error) {
	path := fmt.Sprintf("/workflow/%s/events", workflowID)
	if sinceSeq > 0 {
		path += fmt.Sprintf("?since=%d", sinceSeq)
	}

	var resp []Event
	if err := c.doRequest(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func (c *Client) ResumeWorkflow(ctx context.Context, workflowID, checkpoint string) (*ResumeResponse, error) {
	var resp ResumeResponse
	req := ResumeRequest{Checkpoint: checkpoint}
	path := fmt.Sprintf("/workflow/%s/resume", workflowID)
	if err := c.doRequest(ctx, http.MethodPost, path, req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (p *ProblemDetails) Error() string {
	if p.Detail != "" {
		return fmt.Sprintf("%s: %s", p.Title, p.Detail)
	}
	return p.Title
}
