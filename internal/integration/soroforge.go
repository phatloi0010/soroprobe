// Package integration provides clients for external contract registries and
// deployment trackers such as SoroForge.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxSoroForgeBytes = 4 << 20

// ErrUnauthorized reports an authentication or authorization failure against SoroForge.
var ErrUnauthorized = errors.New("soroforge: authentication failed")

// TrackedContract is a contract deployment record returned by SoroForge.
type TrackedContract struct {
	Name       string `json:"name"`
	ContractID string `json:"contract_id"`
	Network    string `json:"network"`
	WasmHash   string `json:"wasm_hash,omitempty"`
	Function   string `json:"function,omitempty"`
}

// SoroForgeSource fetches tracked contracts for a network.
type SoroForgeSource interface {
	Contracts(ctx context.Context, network string) ([]TrackedContract, error)
}

// SoroForgeClient queries a SoroForge HTTP API.
type SoroForgeClient struct {
	baseURL string
	token   string
	http    *http.Client
}

var _ SoroForgeSource = (*SoroForgeClient)(nil)

// NewSoroForge builds a SoroForge client for baseURL.
func NewSoroForge(baseURL, token string, client *http.Client, timeout time.Duration) (*SoroForgeClient, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("soroforge url %q must be an absolute http:// or https:// URL", baseURL)
	}
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &SoroForgeClient{
		baseURL: u.String(),
		token:   strings.TrimSpace(token),
		http:    client,
	}, nil
}

type soroForgePage struct {
	Contracts  []TrackedContract `json:"contracts"`
	NextCursor string            `json:"next_cursor,omitempty"`
	Error      string            `json:"error,omitempty"`
}

// Contracts fetches all pages of tracked contracts for network.
func (c *SoroForgeClient) Contracts(ctx context.Context, network string) ([]TrackedContract, error) {
	var all []TrackedContract
	cursor := ""

	for page := 0; page < 100; page++ {
		q := url.Values{}
		if network != "" {
			q.Set("network", network)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		endpoint := c.baseURL + "/api/contracts"
		if encoded := q.Encode(); encoded != "" {
			endpoint += "?" + encoded
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("soroforge: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxSoroForgeBytes))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("soroforge: reading response: %w", readErr)
		}

		switch resp.StatusCode {
		case http.StatusOK:
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, fmt.Errorf("%w (HTTP %d): %s", ErrUnauthorized, resp.StatusCode, extractSoroForgeError(body, resp.Status))
		default:
			return nil, fmt.Errorf("soroforge: HTTP %d: %s", resp.StatusCode, extractSoroForgeError(body, resp.Status))
		}

		var decoded soroForgePage
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("soroforge: decoding response: %w", err)
		}
		all = append(all, decoded.Contracts...)
		if decoded.NextCursor == "" {
			break
		}
		cursor = decoded.NextCursor
	}

	return all, nil
}

func extractSoroForgeError(body []byte, fallback string) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return fallback
}
