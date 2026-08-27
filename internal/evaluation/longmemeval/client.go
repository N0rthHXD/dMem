package longmemeval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/N0rthHXD/dMem/internal/domain"
)

type MemoryClient interface {
	CreateSpace(context.Context, string, string, string) (domain.Space, error)
	Remember(context.Context, string, rememberRequest) (rememberResult, error)
	Recall(context.Context, recallRequest) (domain.SearchResponse, error)
	WaitForSemanticReady(context.Context, time.Duration) error
}

type Client struct {
	http     *http.Client
	callerID string
	baseURL  string
}

type rememberRequest struct {
	SpaceID       string         `json:"space_id"`
	Content       string         `json:"content"`
	MemoryType    string         `json:"memory_type"`
	SourceName    string         `json:"source_name"`
	SourceEventID string         `json:"source_event_id"`
	OccurredAt    string         `json:"occurred_at"`
	Metadata      map[string]any `json:"metadata"`
	Reason        string         `json:"reason"`
}

type rememberResult struct {
	Memory  domain.Memory `json:"memory"`
	EventID string        `json:"event_id"`
}

type recallRequest struct {
	Query            string   `json:"query"`
	SpaceIDs         []string `json:"space_ids"`
	IncludeAncestors bool     `json:"include_ancestors"`
	ReferenceTime    string   `json:"reference_time"`
	Mode             string   `json:"mode"`
	Limit            int      `json:"limit"`
	MaxChars         int      `json:"max_chars"`
	IncludeEvidence  bool     `json:"include_evidence"`
}

func NewUnixClient(socket, callerID string, timeout time.Duration) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	return &Client{http: &http.Client{Transport: transport, Timeout: timeout}, callerID: callerID, baseURL: "http://localhost"}
}

func NewClient(httpClient *http.Client, baseURL, callerID string) *Client {
	return &Client{http: httpClient, callerID: callerID, baseURL: baseURL}
}

func (c *Client) CreateSpace(ctx context.Context, key, externalID, displayName string) (domain.Space, error) {
	request := struct {
		SpaceType   string `json:"space_type"`
		ExternalID  string `json:"external_id"`
		DisplayName string `json:"display_name"`
	}{"user", externalID, displayName}
	var result domain.Space
	err := c.post(ctx, "/v1/spaces", key, request, &result)
	return result, err
}

func (c *Client) Remember(ctx context.Context, key string, request rememberRequest) (rememberResult, error) {
	var result rememberResult
	err := c.post(ctx, "/v1/remember", key, request, &result)
	return result, err
}

func (c *Client) Recall(ctx context.Context, request recallRequest) (domain.SearchResponse, error) {
	var result domain.SearchResponse
	err := c.post(ctx, "/v1/recall", "", request, &result)
	return result, err
}

type healthResponse struct {
	SemanticState string `json:"semantic_state"`
	JobQueueDepth int    `json:"job_queue_depth"`
}

func (c *Client) Health(ctx context.Context) (healthResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/health", nil)
	if err != nil {
		return healthResponse{}, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return healthResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return healthResponse{}, fmt.Errorf("dMem /v1/health returned %d: %s", response.StatusCode, message)
	}
	var result healthResponse
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&result); err != nil {
		return healthResponse{}, fmt.Errorf("decode dMem health response: %w", err)
	}
	return result, nil
}

func (c *Client) TriggerSemanticRebuild(ctx context.Context) error {
	body, _ := json.Marshal(map[string]any{})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/indexes/semantic/rebuild", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Caller-ID", c.callerID)
	request.Header.Set("Idempotency-Key", fmt.Sprintf("semantic-rebuild-%d", time.Now().UnixNano()))
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("dMem /v1/indexes/semantic/rebuild returned %d: %s", response.StatusCode, message)
	}
	io.Copy(io.Discard, response.Body)
	return nil
}

func (c *Client) WaitForSemanticReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	rebuildTriggered := false
	for {
		health, err := c.Health(ctx)
		if err != nil {
			return err
		}
		if health.SemanticState == "ready" && health.JobQueueDepth == 0 {
			if !rebuildTriggered {
				if err := c.TriggerSemanticRebuild(ctx); err != nil {
					return err
				}
				rebuildTriggered = true
				select {
				case <-time.After(2 * time.Second):
				case <-ctx.Done():
					return ctx.Err()
				}
				continue
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for semantic index: state=%s queue=%d", health.SemanticState, health.JobQueueDepth)
		}
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *Client) post(ctx context.Context, path, key string, body, target any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Caller-ID", c.callerID)
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("dMem %s returned %d: %s", path, response.StatusCode, message)
	}
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode dMem %s response: %w", path, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode dMem %s response: trailing JSON", path)
	}
	return nil
}
