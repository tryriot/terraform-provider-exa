// Package exa is a client for the Exa Team Management API.
package exa

import (
	"bytes"
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

const DefaultBaseURL = "https://admin-api.exa.ai/team-management"

type Client struct {
	BaseURL    string
	APIKey     string
	UserAgent  string
	HTTPClient *http.Client
}

// NewHTTPClient doesn't follow redirects: Go would forward x-api-key to any host.
func NewHTTPClient() *http.Client {
	return &http.Client{
		Timeout: time.Minute,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

type APIKey struct {
	PublicID    string `json:"publicId"`
	Name        string `json:"name"`
	RateLimit   *int64 `json:"rateLimit"`
	BudgetCents *int64 `json:"budgetCents"`
	TeamID      string `json:"teamId"`
	CreatedAt   string `json:"createdAt"`

	PreviousSecretExpiresAt *string `json:"previousSecretExpiresAt"`

	// Returned as `secret` on create, and as `id` by every endpoint.
	Secret string `json:"-"`
}

func (k *APIKey) UnmarshalJSON(b []byte) error {
	type plain APIKey
	var raw struct {
		plain
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*k = APIKey(raw.plain)
	k.Secret = raw.Secret
	if k.Secret == "" {
		k.Secret = raw.ID
	}
	return nil
}

type CreateAPIKeyRequest struct {
	Name        *string `json:"name,omitempty"`
	RateLimit   *int64  `json:"rateLimit,omitempty"`
	BudgetCents *int64  `json:"budgetCents,omitempty"`
}

type UpdateAPIKeyRequest struct {
	Name         *string
	RateLimit    *int64
	BudgetCents  *int64
	RemoveBudget bool
}

func (r UpdateAPIKeyRequest) MarshalJSON() ([]byte, error) {
	body := map[string]any{}
	if r.Name != nil {
		body["name"] = *r.Name
	}
	if r.RateLimit != nil {
		body["rateLimit"] = *r.RateLimit
	}
	if r.BudgetCents != nil {
		body["budgetCents"] = *r.BudgetCents
	} else if r.RemoveBudget {
		body["budgetCents"] = nil
	}
	return json.Marshal(body)
}

type Error struct {
	StatusCode int
	Message    string
	Code       string
}

func (e *Error) Error() string {
	return fmt.Sprintf("exa: %d %s: %s", e.StatusCode, http.StatusText(e.StatusCode), e.Message)
}

func IsNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == http.StatusNotFound
}

type apiKeyEnvelope struct {
	APIKey APIKey `json:"apiKey"`
}

func (c *Client) CreateAPIKey(ctx context.Context, req CreateAPIKeyRequest) (*APIKey, error) {
	var out apiKeyEnvelope
	if err := c.do(ctx, http.MethodPost, "/api-keys", req, &out); err != nil {
		return nil, err
	}
	return &out.APIKey, nil
}

func (c *Client) GetAPIKey(ctx context.Context, id string) (*APIKey, error) {
	var out apiKeyEnvelope
	if err := c.do(ctx, http.MethodGet, keyPath(id), nil, &out); err != nil {
		return nil, err
	}
	return &out.APIKey, nil
}

func (c *Client) UpdateAPIKey(ctx context.Context, id string, req UpdateAPIKeyRequest) (*APIKey, error) {
	var out apiKeyEnvelope
	if err := c.do(ctx, http.MethodPut, keyPath(id), req, &out); err != nil {
		return nil, err
	}
	return &out.APIKey, nil
}

func (c *Client) RotateAPIKey(ctx context.Context, id string, gracePeriodSeconds int64) (*APIKey, error) {
	var out apiKeyEnvelope
	body := map[string]int64{"gracePeriodSeconds": gracePeriodSeconds}
	if err := c.do(ctx, http.MethodPost, keyPath(id)+"/rotate", body, &out); err != nil {
		return nil, err
	}
	return &out.APIKey, nil
}

func (c *Client) DeleteAPIKey(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, keyPath(id), nil, nil)
}

func keyPath(id string) string {
	return "/api-keys/" + url.PathEscape(id)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		msg := strings.TrimSpace(string(respBody))
		if json.Unmarshal(respBody, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &Error{StatusCode: resp.StatusCode, Message: msg, Code: e.Code}
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("exa: decoding %s %s response: %w", method, path, err)
	}
	return nil
}
