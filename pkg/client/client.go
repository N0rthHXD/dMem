// Package client is the Go application client for the dMem Agent Tool Route.
//
// It maps the versioned pkg/toolcontract wire DTOs to the application HTTP API,
// propagates caller, request, correlation, and idempotency identity, and
// preserves dMem structured errors. It is reused in-process by the dmem-mcp
// adapter and can be reused by native Go integrations.
//
// The client owns no domain rules: it never decides memory authority,
// validation, policy, scope visibility, ranking, or job scheduling, and it
// never imports storage, jobs, or provider code.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/N0rthHXD/dMem/pkg/toolcontract"
)

const (
	// DefaultMaxAttempts is the total number of attempts for one retryable call.
	DefaultMaxAttempts = 3
	// DefaultRetryBaseDelay is the first retry delay before exponential growth.
	DefaultRetryBaseDelay = 100 * time.Millisecond
	// DefaultRetryMaxDelay caps a single retry delay.
	DefaultRetryMaxDelay = 2 * time.Second
	// maxResponseBytes bounds one application response body.
	maxResponseBytes = 4 << 20
)

// Caller is the stable application caller identity sent on every request.
type Caller struct {
	SubjectID string
	ClientID  string
}

// RetryPolicy bounds automatic retries of retryable failures.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// Config configures one application client.
type Config struct {
	// Endpoint is the dMem application base URL, for example
	// http://127.0.0.1:7373.
	Endpoint string
	// Caller identifies the client to dMem. At least one field is required.
	Caller Caller
	// Timeout bounds one logical operation including retries. Zero disables it.
	Timeout time.Duration
	// Retry configures automatic retries.
	Retry RetryPolicy
	// HTTPClient overrides the transport. Its Timeout must stay zero so the
	// context owns cancellation.
	HTTPClient *http.Client
	// Logger receives request metadata only. Message bodies, memory content,
	// queries, and errors are never logged. A nil logger disables logging.
	Logger *slog.Logger
	// Now overrides the clock in tests.
	Now func() time.Time
	// NewID overrides request identity generation in tests.
	NewID func() string
}

// Call carries per-invocation protocol identity. Empty fields are generated.
type Call struct {
	RequestID      string
	CorrelationID  string
	IdempotencyKey string
}

// Client is a serializable application client. It is safe for concurrent use.
type Client struct {
	endpoint  *url.URL
	caller    Caller
	timeout   time.Duration
	retry     RetryPolicy
	http      *http.Client
	logger    *slog.Logger
	now       func() time.Time
	newID     func() string
	idCounter uint64
}

// New validates the configuration and returns a client.
func New(config Config) (*Client, error) {
	endpoint, err := url.Parse(strings.TrimSpace(config.Endpoint))
	if err != nil {
		return nil, fmt.Errorf("client: invalid endpoint: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, errors.New("client: endpoint scheme must be http or https")
	}
	if endpoint.Host == "" {
		return nil, errors.New("client: endpoint host is required")
	}
	if endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("client: endpoint must not carry a query or fragment")
	}
	if strings.TrimSpace(config.Caller.SubjectID) == "" && strings.TrimSpace(config.Caller.ClientID) == "" {
		return nil, errors.New("client: caller subject or client identity is required")
	}
	if config.Timeout < 0 || config.Timeout > time.Hour {
		return nil, errors.New("client: timeout must be between zero and one hour")
	}
	retry := config.Retry
	if retry.MaxAttempts == 0 {
		retry.MaxAttempts = DefaultMaxAttempts
	}
	if retry.MaxAttempts < 1 || retry.MaxAttempts > 10 {
		return nil, errors.New("client: retry max attempts must be between 1 and 10")
	}
	if retry.BaseDelay == 0 {
		retry.BaseDelay = DefaultRetryBaseDelay
	}
	if retry.MaxDelay == 0 {
		retry.MaxDelay = DefaultRetryMaxDelay
	}
	if retry.BaseDelay < 0 || retry.MaxDelay < retry.BaseDelay {
		return nil, errors.New("client: retry delays are invalid")
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	if httpClient.Timeout != 0 {
		return nil, errors.New("client: HTTPClient.Timeout must be zero; use Config.Timeout")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	newID := config.NewID
	c := &Client{
		endpoint: endpoint,
		caller:   Caller{SubjectID: strings.TrimSpace(config.Caller.SubjectID), ClientID: strings.TrimSpace(config.Caller.ClientID)},
		timeout:  config.Timeout,
		retry:    retry,
		http:     httpClient,
		logger:   config.Logger,
		now:      now,
		newID:    newID,
	}
	if c.newID == nil {
		c.newID = c.generateID
	}
	return c, nil
}

// Capabilities reads the published tool contract capabilities.
func (c *Client) Capabilities(ctx context.Context, call Call) (toolcontract.Capabilities, error) {
	var output toolcontract.Capabilities
	err := c.exchange(ctx, call, exchange{
		method:  http.MethodGet,
		path:    "/v1/capabilities",
		outName: toolcontract.SchemaCapabilities,
		output:  &output,
	})
	return output, err
}

// Recall issues one bounded read-only recall.
func (c *Client) Recall(ctx context.Context, call Call, input toolcontract.RecallInput) (toolcontract.RecallOutput, error) {
	var output toolcontract.RecallOutput
	raw, err := json.Marshal(input)
	if err != nil {
		return output, localError("recall request could not be encoded")
	}
	err = c.exchange(ctx, call, exchange{
		method:  http.MethodPost,
		path:    "/v1/recall",
		inName:  toolcontract.SchemaRecallInput,
		body:    raw,
		outName: toolcontract.SchemaRecallOutput,
		output:  &output,
	})
	return output, err
}

// Remember issues one atomic memory commit.
func (c *Client) Remember(ctx context.Context, call Call, input toolcontract.MemoryCommitInput) (toolcontract.MemoryCommitOutput, error) {
	var output toolcontract.MemoryCommitOutput
	raw, err := json.Marshal(input)
	if err != nil {
		return output, localError("memory commit request could not be encoded")
	}
	err = c.exchange(ctx, call, exchange{
		method:      http.MethodPost,
		path:        "/v1/memory-commits",
		inName:      toolcontract.SchemaMemoryCommitInput,
		body:        raw,
		outName:     toolcontract.SchemaMemoryCommitOutput,
		output:      &output,
		idempotency: true,
	})
	return output, err
}

// Feedback appends one closed-category feedback event to a memory revision.
func (c *Client) Feedback(ctx context.Context, call Call, input toolcontract.FeedbackInput) (toolcontract.FeedbackOutput, error) {
	var output toolcontract.FeedbackOutput
	raw, err := json.Marshal(input)
	if err != nil {
		return output, localError("feedback request could not be encoded")
	}
	err = c.exchange(ctx, call, exchange{
		method:      http.MethodPost,
		path:        "/v1/feedback",
		inName:      toolcontract.SchemaFeedbackInput,
		body:        raw,
		outName:     toolcontract.SchemaFeedbackOutput,
		output:      &output,
		idempotency: true,
	})
	return output, err
}

// Forget logically deletes one memory at its expected revision.
func (c *Client) Forget(ctx context.Context, call Call, input toolcontract.ForgetInput) (toolcontract.ForgetOutput, error) {
	var output toolcontract.ForgetOutput
	raw, err := json.Marshal(input)
	if err != nil {
		return output, localError("forget request could not be encoded")
	}
	err = c.exchange(ctx, call, exchange{
		method:      http.MethodPost,
		path:        "/v1/forget",
		inName:      toolcontract.SchemaForgetInput,
		body:        raw,
		outName:     toolcontract.SchemaForgetOutput,
		output:      &output,
		idempotency: true,
	})
	return output, err
}

// EnsureScope resolves or creates a scope by stable external identity.
func (c *Client) EnsureScope(ctx context.Context, call Call, input toolcontract.ScopeEnsureInput) (toolcontract.ScopeEnsureOutput, error) {
	var output toolcontract.ScopeEnsureOutput
	raw, err := json.Marshal(input)
	if err != nil {
		return output, localError("scope ensure request could not be encoded")
	}
	err = c.exchange(ctx, call, exchange{
		method:      http.MethodPost,
		path:        "/v1/scopes/ensure",
		inName:      toolcontract.SchemaScopeEnsureInput,
		body:        raw,
		outName:     toolcontract.SchemaScopeEnsureOutput,
		output:      &output,
		idempotency: true,
	})
	return output, err
}

// GetScope reads one scope by its application identifier.
func (c *Client) GetScope(ctx context.Context, call Call, scopeID string) (toolcontract.ScopeOutput, error) {
	var output toolcontract.ScopeOutput
	if strings.TrimSpace(scopeID) == "" {
		return output, localError("scope id is required")
	}
	err := c.exchange(ctx, call, exchange{
		method:  http.MethodGet,
		path:    "/v1/scopes/" + scopeID,
		outName: toolcontract.SchemaScopeOutput,
		output:  &output,
	})
	return output, err
}

// exchange is one protocol-neutral application request.
type exchange struct {
	method      string
	path        string
	inName      string
	body        []byte
	outName     string
	output      any
	idempotency bool
}

func (c *Client) exchange(ctx context.Context, call Call, request exchange) error {
	if request.inName != "" {
		if err := toolcontract.Validate(request.inName, request.body); err != nil {
			return localError("request does not match the dMem tool contract")
		}
	}
	requestID := strings.TrimSpace(call.RequestID)
	if requestID == "" {
		requestID = c.newID()
	}
	idempotencyKey := strings.TrimSpace(call.IdempotencyKey)
	if request.idempotency && idempotencyKey == "" {
		idempotencyKey = c.newID()
	}

	operationCtx := ctx
	if c.timeout > 0 {
		var cancel context.CancelFunc
		operationCtx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}

	delay := c.retry.BaseDelay
	var lastErr error
	for attempt := 1; attempt <= c.retry.MaxAttempts; attempt++ {
		started := c.now()
		status, body, err := c.attempt(operationCtx, request, requestID, call.CorrelationID, idempotencyKey)
		if err == nil {
			err = c.decode(request.outName, body, request.output)
			if err == nil {
				c.log(request.method, request.path, status, requestID, attempt, time.Since(started))
				return nil
			}
		}
		if operationCtx.Err() != nil {
			return contextError(operationCtx.Err())
		}
		var clientErr *Error
		retryable := false
		if errors.As(err, &clientErr) {
			retryable = clientErr.Retryable
		} else {
			// A transport failure is always retryable and never exposes the
			// underlying network error text to callers.
			retryable = true
			err = &Error{Code: CodeCapabilityUnavailable, Message: "dMem endpoint is unavailable", Retryable: true, cause: err}
		}
		lastErr = err
		if !retryable || attempt == c.retry.MaxAttempts {
			return err
		}
		if clientErr != nil && clientErr.RetryAfter > 0 && clientErr.RetryAfter > delay {
			delay = clientErr.RetryAfter
		}
		if delay > c.retry.MaxDelay {
			delay = c.retry.MaxDelay
		}
		if err := sleepContext(operationCtx, delay); err != nil {
			return contextError(err)
		}
		delay *= 2
	}
	return lastErr
}

// attempt performs one HTTP round trip. A zero status with a nil body error
// reports a transport failure; a non-2xx status returns a *Error.
func (c *Client) attempt(ctx context.Context, request exchange, requestID, correlationID, idempotencyKey string) (int, []byte, error) {
	var reader io.Reader
	if request.body != nil {
		reader = bytes.NewReader(request.body)
	}
	target := *c.endpoint
	target.Path = strings.TrimSuffix(c.endpoint.Path, "/") + request.path
	// Drop any pre-escaped form so URL.String re-encodes the composed path.
	target.RawPath = ""
	httpRequest, err := http.NewRequestWithContext(ctx, request.method, target.String(), reader)
	if err != nil {
		return 0, nil, err
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("X-Request-ID", requestID)
	if correlationID != "" {
		httpRequest.Header.Set("X-Correlation-ID", correlationID)
	}
	if c.caller.SubjectID != "" {
		httpRequest.Header.Set("X-Caller-Subject-ID", c.caller.SubjectID)
	}
	if c.caller.ClientID != "" {
		httpRequest.Header.Set("X-Caller-Client-ID", c.caller.ClientID)
	}
	if request.body != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	if request.idempotency {
		httpRequest.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := c.http.Do(httpRequest)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return response.StatusCode, nil, err
	}
	if len(body) > maxResponseBytes {
		return response.StatusCode, nil, &Error{Code: CodeInternal, Message: "dMem response exceeds the size limit", Status: response.StatusCode}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, body, parseError(response.StatusCode, body)
	}
	return response.StatusCode, body, nil
}

func (c *Client) decode(name string, body []byte, output any) error {
	if len(body) == 0 {
		return &Error{Code: CodeInternal, Message: "dMem returned an empty response"}
	}
	if name != "" {
		if err := toolcontract.Validate(name, body); err != nil {
			return &Error{Code: CodeInternal, Message: "dMem returned a response outside the tool contract"}
		}
	}
	if err := json.Unmarshal(body, output); err != nil {
		return &Error{Code: CodeInternal, Message: "dMem returned an unreadable response"}
	}
	return nil
}

func (c *Client) log(method, path string, status int, requestID string, attempt int, duration time.Duration) {
	if c.logger == nil {
		return
	}
	c.logger.DebugContext(context.Background(), "dmem application request",
		"method", method, "path", path, "status", status,
		"request_id", requestID, "attempt", attempt, "duration_ms", duration.Milliseconds())
}

func (c *Client) generateID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		value := atomic.AddUint64(&c.idCounter, 1)
		stamp := uint64(c.now().UnixNano())
		for i := 0; i < 8; i++ {
			b[i] = byte(stamp >> (8 * i))
			b[8+i] = byte(value >> (8 * i))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
