package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cloudraya/terraform-provider-cloudraya/internal/client"
)

const (
	regionA = "01aaaaaaaaaaaaaaaaaaaaaaaa"
	regionB = "01bbbbbbbbbbbbbbbbbbbbbbbb"
)

// catalogFixture covers the cases findProduct has to tell apart: one name
// offered in one region only, two products sharing a name in different
// regions, two sharing a name in the same region, and a product that is
// listed in a region but switched off there.
const catalogFixture = `{"code":200,"data":{"products":[
 {"id":"pkg-a","name":"Small","product_type":{"code":"vm"},
  "product_regions":[{"region_id":"` + regionA + `","is_active":1}]},
 {"id":"tpl-a","name":"Ubuntu","product_type":{"code":"template"},
  "product_regions":[{"region_id":"` + regionA + `","is_active":1}]},
 {"id":"tpl-b","name":"Ubuntu","product_type":{"code":"template"},
  "product_regions":[{"region_id":"` + regionB + `","is_active":1}]},
 {"id":"dup-1","name":"Twin","product_type":{"code":"vm"},
  "product_regions":[{"region_id":"` + regionA + `","is_active":1}]},
 {"id":"dup-2","name":"Twin","product_type":{"code":"vm"},
  "product_regions":[{"region_id":"` + regionA + `","is_active":1}]},
 {"id":"off-1","name":"Retired","product_type":{"code":"vm"},
  "product_regions":[{"region_id":"` + regionA + `","is_active":0}]},
 {"id":"disk-1","name":"Disk-50","product_type":{"code":"storage"},
  "product_regions":[{"region_id":"` + regionB + `","is_active":1}]}
]}}`

func catalogClient(t *testing.T) *client.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/core/v1/auth/login":
			payload, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
			token := "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
			fmt.Fprintf(w, `{"code":200,"data":{"token":%q}}`, token)
		case strings.HasSuffix(r.URL.Path, "/v1/products"):
			w.Write([]byte(catalogFixture))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":404,"message":"not found"}`))
		}
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(client.Config{Email: "dev@example.com", Password: "secret", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("client.New() error = %v", err)
	}
	return c
}

func TestFindProduct(t *testing.T) {
	c := catalogClient(t)

	tests := []struct {
		name       string
		typeCode   string
		product    string
		region     string
		wantID     string
		wantErrHas string
	}{
		{name: "found in region", typeCode: "vm", product: "Small", region: regionA, wantID: "pkg-a"},
		{name: "same name, picked by region A", typeCode: "template", product: "Ubuntu", region: regionA, wantID: "tpl-a"},
		{name: "same name, picked by region B", typeCode: "template", product: "Ubuntu", region: regionB, wantID: "tpl-b"},
		{name: "no region given matches by name", typeCode: "storage", product: "Disk-50", wantID: "disk-1"},
		{name: "exists but not in region", typeCode: "vm", product: "Small", region: regionB, wantErrHas: "not active in region"},
		{name: "listed but switched off", typeCode: "vm", product: "Retired", region: regionA, wantErrHas: "not active in region"},
		{name: "name not found", typeCode: "vm", product: "Huge", region: regionA, wantErrHas: "no vm product named"},
		{name: "type must match too", typeCode: "template", product: "Small", region: regionA, wantErrHas: "no template product named"},
		{name: "two active matches", typeCode: "vm", product: "Twin", region: regionA, wantErrHas: "2 vm products"},
		{name: "two templates, no region to split them", typeCode: "template", product: "Ubuntu", wantErrHas: "2 template products"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := findProduct(context.Background(), c, "proj", tt.typeCode, tt.product, tt.region)
			if tt.wantErrHas != "" {
				if err == nil {
					t.Fatalf("findProduct() = %q, want error containing %q", p.ID, tt.wantErrHas)
				}
				if !strings.Contains(err.Error(), tt.wantErrHas) {
					t.Fatalf("error = %q, want it to contain %q", err, tt.wantErrHas)
				}
				return
			}
			if err != nil {
				t.Fatalf("findProduct() error = %v", err)
			}
			if p.ID != tt.wantID {
				t.Errorf("findProduct() = %q, want %q", p.ID, tt.wantID)
			}
		})
	}
}
