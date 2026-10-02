// Package client is a thin HTTP client for the CloudRaya v2 API.
//
// Every CloudRaya microservice is published under its own prefix on the same
// host (https://api-v2.cloudraya.com/<service>), and every response shares one
// envelope: {"code":200,"message":"...","data":{...}}.
//
// Authentication is bearer-only: credentials are exchanged once at
// /core/v1/auth/login for a JWT, which is then sent on every other call. The
// API rejects HTTP Basic authentication outright.
package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Service prefixes on the API host.
const (
	ServiceCore          = "core"
	ServiceVM            = "vm"
	ServiceNetwork       = "network"
	ServiceStorage       = "storage"
	ServiceObjectStorage = "object-storage"
	ServiceKubernetes    = "kubernetes"
	ServiceRegistry      = "image-registry"
	ServiceProduct       = "product"

	// ServiceProductCatalog ("product_2") is the same catalog API served under
	// a second prefix on some deployments. DoCatalog tries ServiceProduct first
	// and falls back to it.
	ServiceProductCatalog = "product_2"
)

// catalogServices is the order DoCatalog tries the catalog prefixes in.
var catalogServices = []string{ServiceProduct, ServiceProductCatalog}

const DefaultBaseURL = "https://api-v2.cloudraya.com"

type Config struct {
	Email     string
	Password  string
	BaseURL   string
	ProjectID string
	Timeout   time.Duration
}

type Client struct {
	email     string
	password  string
	baseURL   string
	projectID string
	http      *http.Client

	mu       sync.Mutex
	token    string
	tokenExp time.Time

	catalogMu      sync.Mutex
	catalogService string
}

// tokenSkew refreshes the token slightly before it actually expires so a
// long-running apply does not fail mid-flight.
const tokenSkew = 60 * time.Second

func New(cfg Config) (*Client, error) {
	if cfg.Email == "" || cfg.Password == "" {
		return nil, fmt.Errorf("email and password are required")
	}
	base := cfg.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	if _, err := url.Parse(base); err != nil {
		return nil, fmt.Errorf("invalid base_url %q: %w", base, err)
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &Client{
		email:     cfg.Email,
		password:  cfg.Password,
		baseURL:   strings.TrimRight(base, "/"),
		projectID: cfg.ProjectID,
		http:      &http.Client{Timeout: timeout},
	}, nil
}

// ProjectID is the provider-level default project, used when a resource does
// not set one of its own.
func (c *Client) ProjectID() string { return c.projectID }

// DoCatalog is Do against the product catalog, whose prefix differs between
// deployments: "product" on most, "product_2" where "product" is absent or
// down. The first prefix that answers is remembered for the client's lifetime.
func (c *Client) DoCatalog(ctx context.Context, method, path string, body, out any) error {
	c.catalogMu.Lock()
	known := c.catalogService
	c.catalogMu.Unlock()
	if known != "" {
		return c.Do(ctx, method, known, path, body, out)
	}

	var firstErr error
	for _, svc := range catalogServices {
		err := c.Do(ctx, method, svc, path, body, out)
		if err == nil {
			c.catalogMu.Lock()
			c.catalogService = svc
			c.catalogMu.Unlock()
			return nil
		}
		if !serviceUnavailable(err) {
			return err
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// serviceUnavailable reports a response meaning "this prefix is not served
// here" (404) or "it is down" (5xx), as opposed to an answer from the service.
func serviceUnavailable(err error) bool {
	var apiErr *Error
	if !asError(err, &apiErr) {
		return false
	}
	return apiErr.StatusCode == http.StatusNotFound || apiErr.StatusCode >= 500
}

// envelope is the shape every CloudRaya endpoint responds with. Validation
// failures put the useful part in "error" — a list of per-field messages —
// while "message" is only a generic "Please Check Your Request".
type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	// "error" is a list of per-field objects on a 422 but a bare string on a
	// 400, so it is decoded lazily rather than into one fixed shape.
	Errors json.RawMessage `json:"error"`
}

type fieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// fieldErrors renders the "error" member whichever shape it arrived in.
func (e envelope) fieldErrors() string {
	if len(e.Errors) == 0 || string(e.Errors) == "null" {
		return ""
	}
	var list []fieldError
	if err := json.Unmarshal(e.Errors, &list); err == nil {
		parts := make([]string, 0, len(list))
		for _, fe := range list {
			switch {
			case fe.Field != "" && fe.Message != "":
				parts = append(parts, fe.Field+": "+fe.Message)
			case fe.Message != "":
				parts = append(parts, fe.Message)
			}
		}
		return strings.Join(parts, "; ")
	}
	var s string
	if err := json.Unmarshal(e.Errors, &s); err == nil {
		return s
	}
	return ""
}

// Error carries the HTTP status so callers can distinguish "gone" (404) from a
// real failure — Terraform reads must remove state on 404 rather than error.
type Error struct {
	StatusCode int
	Message    string
	Body       string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("cloudraya: %s (HTTP %d)", e.Message, e.StatusCode)
	}
	return fmt.Sprintf("cloudraya: HTTP %d: %s", e.StatusCode, e.Body)
}

func IsNotFound(err error) bool {
	var apiErr *Error
	if ok := asError(err, &apiErr); ok {
		return apiErr.StatusCode == http.StatusNotFound
	}
	return false
}

func asError(err error, target **Error) bool {
	for err != nil {
		if e, ok := err.(*Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Do performs a request against one service and unmarshals the envelope's
// "data" field into out. out may be nil when the response body is not needed.
//
// A 401 is retried once with a fresh token: tokens expire on a wall clock, so
// a long apply can outlive one that was valid when the operation started.
func (c *Client) Do(ctx context.Context, method, service, path string, body, out any) error {
	err := c.do(ctx, method, service, path, body, out)
	if err != nil && isUnauthorized(err) {
		c.invalidateToken()
		return c.do(ctx, method, service, path, body, out)
	}
	return err
}

func (c *Client) do(ctx context.Context, method, service, path string, body, out any) error {
	token, err := c.ensureToken(ctx)
	if err != nil {
		return err
	}
	return c.raw(ctx, method, service, path, body, out, token)
}

// raw issues a single request. An empty token omits the Authorization header,
// which is what the login call itself needs.
func (c *Client) raw(ctx context.Context, method, service, path string, body, out any, token string) error {
	endpoint := fmt.Sprintf("%s/%s%s", c.baseURL, service, path)

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("calling %s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response from %s %s: %w", method, endpoint, err)
	}

	var env envelope
	// A non-JSON body (gateway error page, proxy 502) must not be reported as a
	// decode failure: the status code is the useful signal.
	decodeErr := json.Unmarshal(raw, &env)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := env.Message
		if decodeErr != nil {
			message = ""
		} else if details := env.fieldErrors(); details != "" && details != message {
			// On a validation failure the per-field messages are what tell the
			// operator what to fix; the top-level message is generic.
			message = message + " (" + details + ")"
		}
		return &Error{StatusCode: resp.StatusCode, Message: message, Body: truncate(string(raw), 500)}
	}
	if decodeErr != nil {
		return fmt.Errorf("decoding response from %s %s: %w (body: %s)",
			method, endpoint, decodeErr, truncate(string(raw), 200))
	}

	if out == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decoding data field from %s %s: %w", method, endpoint, err)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func isUnauthorized(err error) bool {
	var apiErr *Error
	if asError(err, &apiErr) {
		return apiErr.StatusCode == http.StatusUnauthorized
	}
	return false
}

func (c *Client) invalidateToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = ""
	c.tokenExp = time.Time{}
}

// ensureToken returns a valid bearer token, logging in if there is none yet or
// the current one is about to expire.
func (c *Client) ensureToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Add(tokenSkew).Before(c.tokenExp) {
		return c.token, nil
	}

	var data struct {
		Token string `json:"token"`
	}
	err := c.raw(ctx, http.MethodPost, ServiceCore, "/v1/auth/login",
		map[string]string{"email": c.email, "password": c.password}, &data, "")
	if err != nil {
		return "", fmt.Errorf("logging in as %s: %w", c.email, err)
	}
	if data.Token == "" {
		return "", fmt.Errorf("logging in as %s: the API returned no token", c.email)
	}

	c.token = data.Token
	c.tokenExp = jwtExpiry(data.Token)
	return c.token, nil
}

// jwtExpiry reads the "exp" claim without verifying the signature — the token
// is opaque to us and only the server validates it; we read exp solely to know
// when to refresh. An unreadable claim falls back to a conservative window.
func jwtExpiry(token string) time.Time {
	const fallback = 30 * time.Minute

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Now().Add(fallback)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Now().Add(fallback)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return time.Now().Add(fallback)
	}
	return time.Unix(claims.Exp, 0)
}
