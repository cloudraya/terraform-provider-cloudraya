package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// moveFromRenamed lets a `moved` block carry state over from the name a
// resource had before it was renamed. The schema did not change with the name,
// so the old state is copied across unchanged.
func moveFromRenamed(ctx context.Context, r resource.Resource, oldTypeName string) []resource.StateMover {
	var s resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &s)
	return []resource.StateMover{{
		SourceSchema: &s.Schema,
		StateMover: func(_ context.Context, req resource.MoveStateRequest, resp *resource.MoveStateResponse) {
			if req.SourceTypeName != oldTypeName || !strings.HasSuffix(req.SourceProviderAddress, "cloudraya/cloudraya") {
				return
			}
			resp.TargetState.Raw = req.SourceState.Raw
		},
	}}
}

// stringListValues flattens a types.List of strings. A null or unknown list
// yields an empty slice rather than an error: callers treat "not set" and
// "empty" the same way.
func stringListValues(ctx context.Context, list types.List) ([]string, diag.Diagnostics) {
	var diags diag.Diagnostics
	if list.IsNull() || list.IsUnknown() {
		return nil, diags
	}
	var out []string
	diags.Append(list.ElementsAs(ctx, &out, false)...)
	return out, diags
}

// optionalString returns the value of an optional attribute, or "" when unset.
func optionalString(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

// splitCIDR splits "10.20.64.0/19" into its address and prefix length.
func splitCIDR(cidr string) (ip, size string, ok bool) {
	ip, size, ok = strings.Cut(cidr, "/")
	return ip, size, ok && ip != "" && size != ""
}

// fillIfEmpty sets an attribute only when it holds no value yet. Read uses it
// for attributes the API reports under a different name: after an import they
// are empty and must be recovered, but otherwise the configured value stands.
func fillIfEmpty(dst *types.String, v string) {
	if v != "" && (dst.IsNull() || dst.IsUnknown()) {
		*dst = types.StringValue(v)
	}
}
