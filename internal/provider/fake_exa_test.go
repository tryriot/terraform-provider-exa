package provider

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/tryriot/terraform-provider-exa/internal/exa"
)

// fakeExa mimics the Team Management API as observed on the real one.
type fakeExa struct {
	mu      sync.Mutex
	url     string
	budgets bool
	seq     int
	keys    map[string]*fakeKey
}

type fakeKey struct {
	publicID, secret, name, createdAt string
	rateLimit, budgetCents            *int64
	previousSecretExpiresAt           time.Time
}

const fakeServiceKey = "fake-service-key"

func newFakeExa(t *testing.T, budgets bool) *fakeExa {
	t.Setenv(APIKeyEnvVar, fakeServiceKey)
	f := &fakeExa{budgets: budgets, keys: map[string]*fakeKey{}}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api-keys", f.create)
	mux.HandleFunc("GET /api-keys/{id}", f.withKey(f.get))
	mux.HandleFunc("PUT /api-keys/{id}", f.withKey(f.update))
	mux.HandleFunc("DELETE /api-keys/{id}", f.withKey(f.delete))
	mux.HandleFunc("POST /api-keys/{id}/rotate", f.withKey(f.rotate))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != fakeServiceKey {
			writeJSON(w, http.StatusUnauthorized, errorBody("Unauthorized"))
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func (f *fakeExa) target() target {
	return target{
		factories: map[string]func() (tfprotov6.ProviderServer, error){
			"exa": providerserver.NewProtocol6WithError(&ExaProvider{version: "test", baseURL: f.url}),
		},
		client: &exa.Client{BaseURL: f.url, APIKey: fakeServiceKey},
	}
}

func (f *fakeExa) deleteAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	clear(f.keys)
}

func (f *fakeExa) create(w http.ResponseWriter, r *http.Request) {
	f.seq++
	k := &fakeKey{
		publicID:  fmt.Sprintf("key_fake%d", f.seq),
		secret:    newFakeSecret(),
		name:      "API Key",
		createdAt: time.Now().UTC().Format(time.RFC3339),
	}
	if !f.apply(w, r, k) {
		return
	}
	f.keys[k.publicID] = k
	writeJSON(w, http.StatusOK, k.response(true))
}

func (f *fakeExa) get(w http.ResponseWriter, _ *http.Request, k *fakeKey) {
	writeJSON(w, http.StatusOK, k.response(false))
}

func (f *fakeExa) update(w http.ResponseWriter, r *http.Request, k *fakeKey) {
	if f.apply(w, r, k) {
		writeJSON(w, http.StatusOK, k.response(false))
	}
}

func (f *fakeExa) delete(w http.ResponseWriter, _ *http.Request, k *fakeKey) {
	delete(f.keys, k.publicID)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (f *fakeExa) rotate(w http.ResponseWriter, r *http.Request, k *fakeKey) {
	var body struct {
		GracePeriodSeconds *int64 `json:"gracePeriodSeconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.GracePeriodSeconds == nil {
		writeJSON(w, http.StatusBadRequest, errorBody("gracePeriodSeconds is required"))
		return
	}
	if time.Now().Before(k.previousSecretExpiresAt) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":                   "The previous secret is still active",
			"code":                    "previous_secret_active",
			"previousSecretExpiresAt": formatFakeTime(k.previousSecretExpiresAt),
		})
		return
	}
	k.secret = newFakeSecret()
	k.previousSecretExpiresAt = time.Now().Add(time.Duration(*body.GracePeriodSeconds) * time.Second)
	writeJSON(w, http.StatusOK, k.response(true))
}

func (f *fakeExa) apply(w http.ResponseWriter, r *http.Request, k *fakeKey) bool {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON"))
		return false
	}
	budget, hasBudget := body["budgetCents"]
	if hasBudget && !f.budgets {
		writeJSON(w, http.StatusForbidden, errorBody("API key budgets feature is not enabled for this team"))
		return false
	}
	rateLimit, hasRateLimit := body["rateLimit"]
	if hasRateLimit && string(rateLimit) == "null" {
		writeJSON(w, http.StatusBadRequest, errorBody("Invalid type for parameter 'rateLimit'. Expected a string but received a object."))
		return false
	}

	if hasBudget {
		k.budgetCents = decodeNullableInt(budget)
	}
	if hasRateLimit {
		k.rateLimit = decodeNullableInt(rateLimit)
	}
	if name, ok := body["name"]; ok {
		_ = json.Unmarshal(name, &k.name)
	}
	return true
}

func (f *fakeExa) withKey(h func(http.ResponseWriter, *http.Request, *fakeKey)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		k, ok := f.keys[r.PathValue("id")]
		if !ok {
			writeJSON(w, http.StatusNotFound, errorBody("API key not found"))
			return
		}
		h(w, r, k)
	}
}

func (k *fakeKey) response(withSecret bool) map[string]any {
	key := map[string]any{
		"id":                      k.secret,
		"publicId":                k.publicID,
		"name":                    k.name,
		"rateLimit":               k.rateLimit,
		"budgetCents":             k.budgetCents,
		"teamId":                  "team_fake",
		"createdAt":               k.createdAt,
		"previousSecretExpiresAt": nil,
	}
	if time.Now().Before(k.previousSecretExpiresAt) {
		key["previousSecretExpiresAt"] = formatFakeTime(k.previousSecretExpiresAt)
	}
	if withSecret {
		key["secret"] = k.secret
	}
	return map[string]any{"apiKey": key}
}

func decodeNullableInt(raw json.RawMessage) *int64 {
	var v *int64
	_ = json.Unmarshal(raw, &v)
	return v
}

func newFakeSecret() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func formatFakeTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func errorBody(msg string) map[string]any {
	return map[string]any{"error": msg}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
