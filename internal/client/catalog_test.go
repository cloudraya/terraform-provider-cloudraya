package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type catalogOut struct {
	Regions []struct {
		ID string `json:"id"`
	} `json:"regions"`
}

// serveCatalog answers /<prefix>/v1/regions with the given status per prefix
// and counts the calls each prefix receives.
func serveCatalog(t *testing.T, status map[string]int) (*Client, map[string]*int32) {
	t.Helper()
	calls := map[string]*int32{"product": new(int32), "product_2": new(int32)}
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		prefix := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)[0]
		if n, ok := calls[prefix]; ok {
			atomic.AddInt32(n, 1)
		}
		code, ok := status[prefix]
		if !ok {
			code = http.StatusNotFound
		}
		w.WriteHeader(code)
		if code == http.StatusOK {
			w.Write([]byte(`{"code":200,"data":{"regions":[{"id":"from-` + prefix + `"}]}}`))
			return
		}
		fmt.Fprintf(w, `{"code":%d,"message":%q}`, code, http.StatusText(code))
	})
	return c, calls
}

func TestDoCatalogPrefersProduct(t *testing.T) {
	c, calls := serveCatalog(t, map[string]int{"product": 200, "product_2": 200})

	var out catalogOut
	if err := c.DoCatalog(context.Background(), http.MethodGet, "/v1/regions", nil, &out); err != nil {
		t.Fatalf("DoCatalog() error = %v", err)
	}
	if got := out.Regions[0].ID; got != "from-product" {
		t.Errorf("served by %q, want product", got)
	}
	if n := atomic.LoadInt32(calls["product_2"]); n != 0 {
		t.Errorf("product_2 called %d times, want 0 when product answers", n)
	}
}

// Where "product" is not routed at all (404) or is down (5xx), the same
// catalog under "product_2" must be used instead.
func TestDoCatalogFallsBack(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			c, _ := serveCatalog(t, map[string]int{"product": code, "product_2": 200})

			var out catalogOut
			if err := c.DoCatalog(context.Background(), http.MethodGet, "/v1/regions", nil, &out); err != nil {
				t.Fatalf("DoCatalog() error = %v", err)
			}
			if got := out.Regions[0].ID; got != "from-product_2" {
				t.Errorf("served by %q, want product_2", got)
			}
		})
	}
}

// A real answer from the service (here a 422) is an answer, not a sign the
// prefix is missing; falling back would hide it.
func TestDoCatalogDoesNotFallBackOnClientError(t *testing.T) {
	c, calls := serveCatalog(t, map[string]int{"product": http.StatusUnprocessableEntity, "product_2": 200})

	err := c.DoCatalog(context.Background(), http.MethodGet, "/v1/regions", nil, &catalogOut{})
	if err == nil {
		t.Fatal("DoCatalog() error = nil, want the 422 surfaced")
	}
	if n := atomic.LoadInt32(calls["product_2"]); n != 0 {
		t.Errorf("product_2 called %d times, want 0 after a 422", n)
	}
}

func TestDoCatalogRemembersWorkingPrefix(t *testing.T) {
	c, calls := serveCatalog(t, map[string]int{"product": http.StatusServiceUnavailable, "product_2": 200})

	for i := 0; i < 3; i++ {
		if err := c.DoCatalog(context.Background(), http.MethodGet, "/v1/regions", nil, &catalogOut{}); err != nil {
			t.Fatalf("DoCatalog() #%d error = %v", i, err)
		}
	}
	if n := atomic.LoadInt32(calls["product"]); n != 1 {
		t.Errorf("product called %d times, want 1 (probe once, then remember product_2)", n)
	}
}

func TestDoCatalogReportsWhenNeitherAnswers(t *testing.T) {
	c, _ := serveCatalog(t, map[string]int{"product": http.StatusServiceUnavailable, "product_2": http.StatusNotFound})

	err := c.DoCatalog(context.Background(), http.MethodGet, "/v1/regions", nil, &catalogOut{})
	if err == nil {
		t.Fatal("DoCatalog() error = nil, want an error when no prefix answers")
	}
}
