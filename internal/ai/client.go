// Package ai is the ONLY package allowed to talk to the Python RAG
// service. It knows nothing about Kubernetes and nothing about how a
// Snapshot was built — it just marshals one to JSON, POSTs it, and
// unmarshals the response.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"time"

	"github.com/Yashmit-k/KubeAssist/backend-go/internal/model"
)

// Client wraps an http.Client with the RAG service's base URL, auth, and
// retry policy.
type Client struct {
	httpClient  *http.Client
	baseURL     string
	authToken   string
	maxRetries  int
	baseBackoff time.Duration
}

// New constructs an ai.Client.
//
// WHY BUILD OUR OWN http.Client HERE INSTEAD OF USING http.DefaultClient?
// http.DefaultClient has NO TIMEOUT AT ALL by default — a well-known Go
// gotcha. Using it in production code means a hung server on the other
// end can block your goroutine forever. We always construct an explicit
// http.Client with a Timeout set. Note this is a REQUEST-level timeout
// (covers connect + write + read of the full response); we layer
// per-attempt context deadlines on top of it for the retry loop, and the
// caller can additionally pass a context with the plan's overall 120s
// troubleshoot budget.
func New(baseURL, authToken string, timeout time.Duration) *Client {
	return &Client{
		httpClient:  &http.Client{Timeout: timeout},
		baseURL:     baseURL,
		authToken:   authToken,
		maxRetries:  1, // plan: "LLM: 1 retry on 429/5xx. No retry storm."
		baseBackoff: 500 * time.Millisecond,
	}
}

// Troubleshoot sends a redacted snapshot to the Python service's
// POST /ai/troubleshoot endpoint and returns the parsed diagnosis.
func (c *Client) Troubleshoot(ctx context.Context, snapshot *model.DiagnosticSnapshot) (*model.DiagnosisResponse, error) {
	body, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("ai: marshaling snapshot: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff with jitter: wait baseBackoff * 2^attempt,
			// plus a small random jitter to avoid multiple retrying
			// clients all hammering the server at exactly the same
			// instant (the "thundering herd" problem). Even with just
			// one retry, this habit is worth building correctly now.
			backoff := time.Duration(float64(c.baseBackoff) * math.Pow(2, float64(attempt-1)))
			jitter := time.Duration(rand.Int63n(int64(backoff) / 2))
			wait := backoff + jitter

			// select on ctx.Done() while waiting means if the CALLER's
			// context gets cancelled during our backoff sleep, we stop
			// immediately instead of sleeping the full duration
			// pointlessly before checking.
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, fmt.Errorf("ai: context cancelled during backoff: %w", ctx.Err())
			}
		}

		resp, err := c.doRequest(ctx, body)
		if err == nil {
			return resp, nil
		}
		lastErr = err

		var httpErr *retryableHTTPError
		if !isRetryable(err, &httpErr) {
			// Non-retryable failure (bad request, auth failure, network
			// error unrelated to server load) — fail immediately rather
			// than burning the retry budget on something retrying can't
			// fix.
			return nil, err
		}
	}

	return nil, fmt.Errorf("ai: exhausted %d retries: %w", c.maxRetries, lastErr)
}

// retryableHTTPError marks a response status that's worth retrying
// (429 or 5xx), as distinct from a plain network/parsing error.
type retryableHTTPError struct {
	StatusCode int
}

func (e *retryableHTTPError) Error() string {
	return fmt.Sprintf("received retryable status %d", e.StatusCode)
}

func isRetryable(err error, target **retryableHTTPError) bool {
	he, ok := err.(*retryableHTTPError)
	if ok {
		*target = he
		return true
	}
	return false
}

func (c *Client) doRequest(ctx context.Context, body []byte) (*model.DiagnosisResponse, error) {
	url := c.baseURL + "/ai/troubleshoot"

	// http.NewRequestWithContext (not http.NewRequest +
	// req.WithContext) attaches the context AT CONSTRUCTION time. This
	// matters for cancellation propagation: the context governs the
	// ENTIRE request lifecycle from the moment it's created, including
	// connection establishment, not just the body read.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ai: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// A transport-level error (connection refused, DNS failure,
		// TIMEOUT) — these are also worth retrying once, since they're
		// often transient (service still starting up in docker-compose,
		// for instance).
		return nil, &retryableHTTPError{StatusCode: 0}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ai: reading response body: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, &retryableHTTPError{StatusCode: resp.StatusCode}
	}
	if resp.StatusCode != http.StatusOK {
		// A 4xx (other than 429) means WE sent something wrong — retrying
		// the identical payload will fail identically, so this is a
		// terminal error, not wrapped in retryableHTTPError.
		return nil, fmt.Errorf("ai: troubleshoot request failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	var diagnosis model.DiagnosisResponse
	if err := json.Unmarshal(respBody, &diagnosis); err != nil {
		return nil, fmt.Errorf("ai: parsing diagnosis response: %w", err)
	}

	return &diagnosis, nil
}
