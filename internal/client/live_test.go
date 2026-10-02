package client

import (
	"context"
	"os"
	"testing"
	"time"
)

// Live tests run only when CLOUDRAYA_EMAIL/PASSWORD are exported; they are
// skipped in CI and on a normal `go test ./...`.
func liveClient(t *testing.T) *Client {
	t.Helper()
	email := os.Getenv("CLOUDRAYA_EMAIL")
	password := os.Getenv("CLOUDRAYA_PASSWORD")
	if email == "" || password == "" {
		t.Skip("set CLOUDRAYA_EMAIL and CLOUDRAYA_PASSWORD to run live tests")
	}
	c, err := New(Config{
		Email:    email,
		Password: password,
		BaseURL:  os.Getenv("CLOUDRAYA_BASE_URL"),
		Timeout:  30 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return c
}

func TestLiveLoginAndListProjects(t *testing.T) {
	c := liveClient(t)

	var out struct {
		Projects []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"projects"`
		Total int `json:"total"`
	}
	if err := c.Do(context.Background(), "GET", ServiceCore, "/v1/projects", nil, &out); err != nil {
		t.Fatalf("listing projects: %v", err)
	}
	if len(out.Projects) == 0 {
		t.Fatal("no projects returned; the account needs at least one to provision into")
	}
	for _, p := range out.Projects {
		t.Logf("project %s (%s)", p.ID, p.Name)
	}

	if c.token == "" {
		t.Error("token was not cached after a successful call")
	}
	if !c.tokenExp.After(time.Now()) {
		t.Errorf("tokenExp = %v, want a future expiry", c.tokenExp)
	}
	t.Logf("token expires %s (in %s)", c.tokenExp.Format(time.RFC3339), time.Until(c.tokenExp).Round(time.Minute))
}
