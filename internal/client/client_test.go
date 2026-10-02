package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// testJWT builds an unsigned token whose exp claim is `in` from now. Only the
// payload matters: the client reads exp and never verifies the signature.
func testJWT(in time.Duration) string {
	payload, _ := json.Marshal(map[string]any{"exp": time.Now().Add(in).Unix()})
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// newTestClient serves the login endpoint automatically; handler sees every
// other request.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *int32) {
	t.Helper()
	var logins int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/core/v1/auth/login" {
			atomic.AddInt32(&logins, 1)
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if body["email"] != "dev@example.com" || body["password"] != "secret" {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"code":401,"message":"Wrong email or password"}`))
				return
			}
			fmt.Fprintf(w, `{"code":200,"message":"Login has been success","data":{"token":%q}}`, testJWT(2*time.Hour))
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	c, err := New(Config{Email: "dev@example.com", Password: "secret", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c, &logins
}

func TestDoSendsBearerTokenAndUnwrapsEnvelope(t *testing.T) {
	c, logins := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/vm/v1/virtual-machines/abc"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got := r.Header.Get("Authorization"); got == "" || got[:7] != "Bearer " {
			t.Errorf("Authorization = %q, want a Bearer token", got)
		}
		if _, _, ok := r.BasicAuth(); ok {
			t.Error("request carried HTTP Basic credentials; the API rejects them")
		}
		w.Write([]byte(`{"code":200,"message":"ok","data":{"id":"abc","hostname":"web-01"}}`))
	})

	var out struct {
		ID       string `json:"id"`
		Hostname string `json:"hostname"`
	}
	if err := c.Do(context.Background(), http.MethodGet, ServiceVM, "/v1/virtual-machines/abc", nil, &out); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if out.ID != "abc" || out.Hostname != "web-01" {
		t.Errorf("out = %+v, want id=abc hostname=web-01", out)
	}
	if n := atomic.LoadInt32(logins); n != 1 {
		t.Errorf("logins = %d, want exactly 1", n)
	}
}

// The token is cached: a second call must not log in again.
func TestDoReusesToken(t *testing.T) {
	c, logins := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"code":200,"message":"ok","data":null}`))
	})

	for i := 0; i < 3; i++ {
		if err := c.Do(context.Background(), http.MethodGet, ServiceCore, "/v1/projects", nil, nil); err != nil {
			t.Fatalf("Do() #%d error = %v", i, err)
		}
	}
	if n := atomic.LoadInt32(logins); n != 1 {
		t.Errorf("logins = %d, want 1 (token should be cached)", n)
	}
}

// An expired token mid-apply must trigger one re-login and a retry.
func TestDoRetriesOnceAfter401(t *testing.T) {
	var calls int32
	c, logins := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"code":401,"message":"Unauthenticated."}`))
			return
		}
		w.Write([]byte(`{"code":200,"message":"ok","data":{"id":"ok"}}`))
	})

	var out struct {
		ID string `json:"id"`
	}
	if err := c.Do(context.Background(), http.MethodGet, ServiceVM, "/v1/virtual-machines", nil, &out); err != nil {
		t.Fatalf("Do() error = %v, want the retry to succeed", err)
	}
	if out.ID != "ok" {
		t.Errorf("out.ID = %q, want the retried response", out.ID)
	}
	if n := atomic.LoadInt32(logins); n != 2 {
		t.Errorf("logins = %d, want 2 (initial + refresh after 401)", n)
	}
}

// A persistent 401 must surface rather than retrying forever.
func TestDoGivesUpOnRepeated401(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":401,"message":"Unauthenticated."}`))
	})

	err := c.Do(context.Background(), http.MethodGet, ServiceVM, "/v1/virtual-machines", nil, nil)
	if err == nil {
		t.Fatal("Do() error = nil, want the 401 to surface")
	}
}

func TestLoginFailureIsReported(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"code":200,"data":null}`))
	})
	c.password = "wrong"

	err := c.Do(context.Background(), http.MethodGet, ServiceCore, "/v1/projects", nil, nil)
	if err == nil {
		t.Fatal("Do() error = nil, want a login failure")
	}
}

func TestDoReportsNotFound(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"code":404,"message":"Virtual machine not found","data":null}`))
	})

	err := c.Do(context.Background(), http.MethodGet, ServiceVM, "/v1/virtual-machines/gone", nil, nil)
	if err == nil {
		t.Fatal("Do() error = nil, want not-found error")
	}
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

func TestIsNotFoundIgnoresOtherStatuses(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"code":422,"message":"hostname already taken","data":null}`))
	})

	err := c.Do(context.Background(), http.MethodPost, ServiceVM, "/v1/virtual-machines", map[string]any{"hostname": "dup"}, nil)
	if err == nil {
		t.Fatal("Do() error = nil, want validation error")
	}
	if IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = true, want false for 422", err)
	}
	var apiErr *Error
	if !asError(err, &apiErr) {
		t.Fatalf("error %v is not *Error", err)
	}
	if apiErr.Message != "hostname already taken" {
		t.Errorf("Message = %q, want the API message surfaced", apiErr.Message)
	}
}

// A proxy or gateway can answer with HTML instead of the API envelope; the
// status code must still drive the outcome rather than a JSON decode failure.
func TestDoHandlesNonJSONErrorBody(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	})

	err := c.Do(context.Background(), http.MethodGet, ServiceVM, "/v1/virtual-machines", nil, nil)
	if err == nil {
		t.Fatal("Do() error = nil, want gateway error")
	}
	var apiErr *Error
	if !asError(err, &apiErr) {
		t.Fatalf("error %v is not *Error", err)
	}
	if apiErr.StatusCode != http.StatusBadGateway {
		t.Errorf("StatusCode = %d, want 502", apiErr.StatusCode)
	}
}

func TestDoSendsJSONBody(t *testing.T) {
	var gotBody string
	var gotContentType string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = string(buf)
		gotContentType = r.Header.Get("Content-Type")
		w.Write([]byte(`{"code":200,"message":"ok","data":{"id":"new"}}`))
	})

	var out struct {
		ID string `json:"id"`
	}
	err := c.Do(context.Background(), http.MethodPost, ServiceStorage, "/v1/volumes",
		map[string]any{"name": "disk-1"}, &out)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotBody != `{"name":"disk-1"}` {
		t.Errorf("body = %q, want the marshalled request", gotBody)
	}
	if out.ID != "new" {
		t.Errorf("out.ID = %q, want new", out.ID)
	}
}

// Delete endpoints commonly answer {"data": null}; that must not be an error.
func TestDoToleratesNullData(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"code":200,"message":"deleted","data":null}`))
	})

	var out struct {
		ID string `json:"id"`
	}
	if err := c.Do(context.Background(), http.MethodDelete, ServiceStorage, "/v1/volumes/abc", nil, &out); err != nil {
		t.Fatalf("Do() error = %v", err)
	}
}

func TestJWTExpiry(t *testing.T) {
	got := jwtExpiry(testJWT(2 * time.Hour))
	want := time.Now().Add(2 * time.Hour)
	if d := got.Sub(want); d > time.Minute || d < -time.Minute {
		t.Errorf("jwtExpiry() = %v, want within a minute of %v", got, want)
	}

	// Malformed tokens must fall back to a near-term refresh, not zero time
	// (which would make every single call re-login).
	for _, bad := range []string{"", "not-a-jwt", "a.!!!.c", "a.e30.c"} {
		if got := jwtExpiry(bad); !got.After(time.Now()) {
			t.Errorf("jwtExpiry(%q) = %v, want a future fallback", bad, got)
		}
	}
}

func TestNewRequiresCredentials(t *testing.T) {
	if _, err := New(Config{Password: "x"}); err == nil {
		t.Error("New() without email error = nil, want error")
	}
	if _, err := New(Config{Email: "x@example.com"}); err == nil {
		t.Error("New() without password error = nil, want error")
	}
}

func TestNewDefaultsBaseURL(t *testing.T) {
	c, err := New(Config{Email: "a@b.c", Password: "p"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if c.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
}
