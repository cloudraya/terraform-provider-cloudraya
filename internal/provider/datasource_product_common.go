package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cloudraya/terraform-provider-cloudraya/internal/client"
)

// productAPI mirrors one entry of GET /v1/products on the product catalog.
// product_regions is the availability list: a product can be globally active
// but still refused by create calls in a region that is not in this list, or
// present but not is_active there.
type productAPI struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	IsActive    int64  `json:"is_active"`
	StatusText  string `json:"status_text"`
	Orderable   int64  `json:"orderable"`
	ProductType struct {
		Code string `json:"code"`
	} `json:"product_type"`
	ProductRegions []struct {
		RegionID   string `json:"region_id"`
		IsActive   int64  `json:"is_active"`
		StatusText string `json:"status_text"`
	} `json:"product_regions"`
}

type productListAPI struct {
	Products []productAPI `json:"products"`
}

// activeInRegion reports whether the product is orderable in regionID.
// regionID == "" skips the check (cloudraya_vm_storage_package makes region_id
// optional).
func (p productAPI) activeInRegion(regionID string) bool {
	if regionID == "" {
		return true
	}
	for _, r := range p.ProductRegions {
		if r.RegionID == regionID && r.IsActive == 1 {
			return true
		}
	}
	return false
}

// findProduct looks up exactly one product of the given type code by name,
// optionally scoped to a region. Returning the mismatch as an error (rather
// than a generic "not found") is the point: a product that exists but is not
// active in the requested region is the single most common cause of a VM,
// volume or VPC create failing opaquely during apply, so the data source
// catches it at plan/read time with a specific, actionable message.
func findProduct(ctx context.Context, c *client.Client, projectID, typeCode, name, regionID string) (*productAPI, error) {
	var list productListAPI
	path := "/v1/products?project_id=" + projectID
	if err := c.DoCatalog(ctx, http.MethodGet, path, nil, &list); err != nil {
		return nil, fmt.Errorf("listing products: %w", err)
	}

	var byName []productAPI
	for _, p := range list.Products {
		if p.ProductType.Code == typeCode && p.Name == name {
			byName = append(byName, p)
		}
	}
	if len(byName) == 0 {
		return nil, fmt.Errorf("no %s product named %q was found", typeCode, name)
	}

	var active []productAPI
	for _, p := range byName {
		if regionID != "" && !p.activeInRegion(regionID) {
			continue
		}
		active = append(active, p)
	}
	if len(active) == 0 {
		if regionID != "" {
			return nil, fmt.Errorf("%s product %q exists but is not active in region %s", typeCode, name, regionID)
		}
		return nil, fmt.Errorf("%s product %q exists but is not active", typeCode, name)
	}
	if len(active) > 1 {
		return nil, fmt.Errorf("%d %s products named %q matched; names are expected to be unique", len(active), typeCode, name)
	}
	return &active[0], nil
}
