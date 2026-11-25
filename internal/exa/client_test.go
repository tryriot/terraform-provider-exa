package exa

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recorded struct {
	method string
	path   string
	uri    string
	apiKey string
	body   map[string]any
	raw    string
}

func newTestClient(t *testing.T, status int, response string) (*Client, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method = r.Method
		rec.path = r.URL.Path
		rec.uri = r.RequestURI
		rec.apiKey = r.Header.Get("x-api-key")
		raw, _ := io.ReadAll(r.Body)
		rec.raw = string(raw)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &rec.body); err != nil {
				t.Errorf("request body is not JSON: %s", raw)
			}
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, APIKey: "service-key", HTTPClient: srv.Client()}, rec
}

func ptr[T any](v T) *T { return &v }

func TestCreateAPIKeyOmitsUnsetFields(t *testing.T) {
	c, rec := newTestClient(t, 200, `{"apiKey":{"id":"s3cr3t","publicId":"key_abc","secret":"s3cr3t","name":"ci","rateLimit":null,"budgetCents":null,"teamId":"team_1","createdAt":"2026-11-25T08:57:09.291Z"}}`)

	key, err := c.CreateAPIKey(context.Background(), CreateAPIKeyRequest{Name: ptr("ci")})
	if err != nil {
		t.Fatal(err)
	}

	if rec.method != http.MethodPost || rec.path != "/api-keys" {
		t.Errorf("got %s %s, want POST /api-keys", rec.method, rec.path)
	}
	if rec.apiKey != "service-key" {
		t.Errorf("x-api-key = %q, want service-key", rec.apiKey)
	}
	if rec.raw != `{"name":"ci"}` {
		t.Errorf("body = %s, want only the name: a team without the budgets feature rejects any budgetCents", rec.raw)
	}
	if key.PublicID != "key_abc" || key.Secret != "s3cr3t" || key.RateLimit != nil || key.TeamID != "team_1" {
		t.Errorf("unexpected key: %+v", key)
	}
}

func TestGetAPIKeyReturnsTheSecretFromID(t *testing.T) {
	c, rec := newTestClient(t, 200, `{"apiKey":{"id":"s3cr3t","publicId":"key_abc","name":"ci","rateLimit":5,"budgetCents":100,"teamId":"team_1","createdAt":"2026-11-25T08:57:09.291Z"}}`)

	key, err := c.GetAPIKey(context.Background(), "key_abc")
	if err != nil {
		t.Fatal(err)
	}

	if rec.method != http.MethodGet || rec.path != "/api-keys/key_abc" {
		t.Errorf("got %s %s, want GET /api-keys/key_abc", rec.method, rec.path)
	}
	if key.Secret != "s3cr3t" {
		t.Errorf("Secret = %q, want the value of id", key.Secret)
	}
	if *key.RateLimit != 5 || *key.BudgetCents != 100 {
		t.Errorf("unexpected key: %+v", key)
	}
}

func TestUpdateAPIKeySendsOnlyChangedFields(t *testing.T) {
	cases := []struct {
		name string
		req  UpdateAPIKeyRequest
		want string
	}{
		{"name only", UpdateAPIKeyRequest{Name: ptr("renamed")}, `{"name":"renamed"}`},
		{"rate limit", UpdateAPIKeyRequest{RateLimit: ptr(int64(10))}, `{"rateLimit":10}`},
		{"set budget", UpdateAPIKeyRequest{BudgetCents: ptr(int64(0))}, `{"budgetCents":0}`},
		{"remove budget", UpdateAPIKeyRequest{RemoveBudget: true}, `{"budgetCents":null}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := newTestClient(t, 200, `{"apiKey":{"publicId":"key_abc","name":"renamed"}}`)

			if _, err := c.UpdateAPIKey(context.Background(), "key_abc", tc.req); err != nil {
				t.Fatal(err)
			}

			if rec.method != http.MethodPut || rec.path != "/api-keys/key_abc" {
				t.Errorf("got %s %s, want PUT /api-keys/key_abc", rec.method, rec.path)
			}
			if rec.raw != tc.want {
				t.Errorf("body = %s, want %s", rec.raw, tc.want)
			}
		})
	}
}

func TestDeleteAPIKey(t *testing.T) {
	c, rec := newTestClient(t, 200, `{"success":true}`)

	if err := c.DeleteAPIKey(context.Background(), "key_abc"); err != nil {
		t.Fatal(err)
	}
	if rec.method != http.MethodDelete || rec.path != "/api-keys/key_abc" {
		t.Errorf("got %s %s, want DELETE /api-keys/key_abc", rec.method, rec.path)
	}
}

func TestErrors(t *testing.T) {
	t.Run("404 is not found", func(t *testing.T) {
		c, _ := newTestClient(t, 404, `{"error":"API key not found"}`)

		_, err := c.GetAPIKey(context.Background(), "key_gone")
		if !IsNotFound(err) {
			t.Fatalf("IsNotFound(%v) = false", err)
		}
	})

	t.Run("other statuses carry the API message", func(t *testing.T) {
		c, _ := newTestClient(t, 403, `{"error":"API key budgets feature is not enabled for this team"}`)

		_, err := c.CreateAPIKey(context.Background(), CreateAPIKeyRequest{BudgetCents: ptr(int64(100))})
		if IsNotFound(err) {
			t.Fatal("403 reported as not found")
		}
		want := "exa: 403 Forbidden: API key budgets feature is not enabled for this team"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})

	t.Run("non-JSON error body", func(t *testing.T) {
		c, _ := newTestClient(t, 502, `<html>bad gateway</html>`)

		_, err := c.GetAPIKey(context.Background(), "key_abc")
		want := "exa: 502 Bad Gateway: <html>bad gateway</html>"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})
}

func TestPathEscapesID(t *testing.T) {
	c, rec := newTestClient(t, 200, `{"apiKey":{"publicId":"key_abc"}}`)

	if _, err := c.GetAPIKey(context.Background(), "../credits"); err != nil {
		t.Fatal(err)
	}
	if rec.uri != "/api-keys/..%2Fcredits" {
		t.Fatalf("request URI = %s, want the id escaped as one path segment", rec.uri)
	}
}

func TestDoesNotFollowRedirects(t *testing.T) {
	var leaked string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("x-api-key")
	}))
	t.Cleanup(elsewhere.Close)
	exa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(exa.Close)

	c := &Client{BaseURL: exa.URL, APIKey: "service-key", HTTPClient: NewHTTPClient()}
	_, err := c.GetAPIKey(context.Background(), "key_abc")

	if leaked != "" {
		t.Fatalf("service key sent to the redirect target")
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusFound {
		t.Fatalf("err = %v, want the 302 reported as an error", err)
	}
}

func TestRotateAPIKey(t *testing.T) {
	c, rec := newTestClient(t, 200, `{"apiKey":{"id":"new","publicId":"key_abc","secret":"new","name":"ci","previousSecretExpiresAt":"2026-11-26T08:00:00.000Z"}}`)

	key, err := c.RotateAPIKey(context.Background(), "key_abc", 3600)
	if err != nil {
		t.Fatal(err)
	}

	if rec.method != http.MethodPost || rec.path != "/api-keys/key_abc/rotate" {
		t.Errorf("got %s %s, want POST /api-keys/key_abc/rotate", rec.method, rec.path)
	}
	if rec.raw != `{"gracePeriodSeconds":3600}` {
		t.Errorf("body = %s", rec.raw)
	}
	if key.Secret != "new" || key.PreviousSecretExpiresAt == nil || *key.PreviousSecretExpiresAt != "2026-11-26T08:00:00.000Z" {
		t.Errorf("unexpected key: %+v", key)
	}
}

func TestErrorCode(t *testing.T) {
	c, _ := newTestClient(t, 409, `{"error":"Previous secret still active","code":"previous_secret_active","previousSecretExpiresAt":"2026-11-26T08:00:00.000Z"}`)

	_, err := c.RotateAPIKey(context.Background(), "key_abc", 0)

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "previous_secret_active" {
		t.Fatalf("err = %#v, want code previous_secret_active", err)
	}
}
