package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/cloudraya/terraform-provider-cloudraya/internal/client"
)

var (
	_ resource.Resource                = (*vpcResource)(nil)
	_ resource.ResourceWithConfigure   = (*vpcResource)(nil)
	_ resource.ResourceWithImportState = (*vpcResource)(nil)
)

func NewVPCResource() resource.Resource { return &vpcResource{} }

type vpcResource struct {
	client *client.Client
}

type vpcModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	RegionID     types.String `tfsdk:"region_id"`
	ProjectID    types.String `tfsdk:"project_id"`
	IPAddress    types.String `tfsdk:"ip_address"`
	NetworkSize  types.String `tfsdk:"network_size"`
	Description  types.String `tfsdk:"description"`
	ForceDestroy types.Bool   `tfsdk:"force_destroy"`

	// Create-time only; never reconciled against the API.
	InitialSubnet types.Object `tfsdk:"initial_subnet"`
	InitialACL    types.Object `tfsdk:"initial_acl"`

	CreatedAt types.String `tfsdk:"created_at"`

	// Reported by the create call, which names the three objects it provisioned.
	InitialSubnetID types.String `tfsdk:"initial_subnet_id"`
	InitialACLID    types.String `tfsdk:"initial_acl_id"`
}

type vpcInitialSubnetModel struct {
	Name        types.String `tfsdk:"name"`
	IPAddress   types.String `tfsdk:"ip_address"`
	NetworkSize types.String `tfsdk:"network_size"`
	Description types.String `tfsdk:"description"`
}

type vpcInitialACLModel struct {
	Name  types.String `tfsdk:"name"`
	Rules types.List   `tfsdk:"rules"`
}

type vpcACLRuleModel struct {
	Protocol    types.String `tfsdk:"protocol"`
	Action      types.String `tfsdk:"action"`
	SourceCIDR  types.String `tfsdk:"source_cidr"`
	StartPort   types.String `tfsdk:"start_port"`
	EndPort     types.String `tfsdk:"end_port"`
	ICMPType    types.String `tfsdk:"icmp_type"`
	ICMPCode    types.String `tfsdk:"icmp_code"`
	TrafficType types.String `tfsdk:"traffic_type"`
}

// flexString decodes a field the API spells as a JSON string on one endpoint and
// a JSON number on another (network_size is sent as "19" but may come back 19).
type flexString struct{ Value string }

func (f *flexString) UnmarshalJSON(b []byte) error {
	raw := strings.TrimSpace(string(b))
	switch {
	case raw == "" || raw == "null":
		f.Value = ""
		return nil
	case raw[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		f.Value = s
		return nil
	default:
		var n json.Number
		if err := json.Unmarshal(b, &n); err != nil {
			return fmt.Errorf("expected a string or number, got %s", truncateJSON(raw))
		}
		f.Value = n.String()
		return nil
	}
}

func truncateJSON(s string) string {
	if len(s) <= 60 {
		return s
	}
	return s[:60] + "..."
}

// vpcAPI mirrors the "data" object returned by the VPC endpoints. The detail
// response reports the region as "region" and the CIDR as a single
// "network_address" ("10.50.0.0/19"); region_id, ip_address and network_size
// are accepted on create but never echoed back, so they are kept as fallbacks.
// Pointers distinguish "absent" from "empty" so a field the API does not echo
// leaves the configured value in state untouched.
type vpcAPI struct {
	ID             string      `json:"id"`
	Name           *string     `json:"name"`
	ProjectID      *string     `json:"project_id"`
	RegionID       *string     `json:"region_id"`
	Region         *string     `json:"region"`
	NetworkAddress *string     `json:"network_address"`
	IPAddress      *string     `json:"ip_address"`
	NetworkSize    *flexString `json:"network_size"`
	Description    *string     `json:"description"`
	CreatedAt      *string     `json:"created_at"`
	Status         *int64      `json:"status"`
	StatusText     *string     `json:"status_text"`
}

// vpcCreateAPI decodes the create response, which does not resemble the detail
// response: create reports the three objects it provisioned as
// {"vpc_id","vpc_network_id","acl_id"} and returns no VPC body.
// The embedded vpcAPI and nested "vpc" are kept as fallbacks in case another
// environment answers with the object itself.
type vpcCreateAPI struct {
	vpcAPI
	VPC          *vpcAPI `json:"vpc"`
	VPCID        string  `json:"vpc_id"`
	VPCNetworkID string  `json:"vpc_network_id"`
	ACLID        string  `json:"acl_id"`
}

func (c *vpcCreateAPI) resolve() *vpcAPI {
	if c.VPCID != "" {
		return &vpcAPI{ID: c.VPCID}
	}
	if c.VPC != nil && c.VPC.ID != "" {
		return c.VPC
	}
	return &c.vpcAPI
}

func (r *vpcResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}

func (r *vpcResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A CloudRaya VPC (supernet). Creating one also provisions an initial subnet " +
			"and ACL; manage further subnets with `cloudraya_vpc_network`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "VPC identifier.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "VPC name. Changing this renames the VPC in place.",
				Required:            true,
			},
			"region_id": schema.StringAttribute{
				MarkdownDescription: "Region to create the VPC in. Changing this forces a new VPC.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Project that owns the VPC. Defaults to the provider's `project_id`. " +
					"Changing this forces a new VPC.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
			"ip_address": schema.StringAttribute{
				MarkdownDescription: "Network address of the supernet, for example `10.20.96.0`. " +
					"Changing this forces a new VPC.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"network_size": schema.StringAttribute{
				MarkdownDescription: "CIDR prefix length of the supernet as a string, for example `\"19\"`. " +
					"Changing this forces a new VPC.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description. The update endpoint accepts `name` only, so " +
					"changing the description forces a new VPC.",
				Optional:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"force_destroy": schema.BoolAttribute{
				MarkdownDescription: "Send `force` on destroy, removing the VPC even when it still holds " +
					"subnets or attached resources. Defaults to `false`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"initial_subnet": schema.SingleNestedAttribute{
				MarkdownDescription: "The first subnet, created alongside the VPC. Used at create time only: " +
					"it is never read back or reconciled, and editing it forces a new VPC. Manage this and " +
					"any further subnets with `cloudraya_vpc_network` once the VPC exists.",
				Optional:      true,
				PlanModifiers: []planmodifier.Object{replaceIfPreviouslySet()},
				Attributes: map[string]schema.Attribute{
					"name":         schema.StringAttribute{MarkdownDescription: "Subnet name.", Required: true},
					"ip_address":   schema.StringAttribute{MarkdownDescription: "Subnet network address.", Required: true},
					"network_size": schema.StringAttribute{MarkdownDescription: "Subnet CIDR prefix length as a string, for example `\"25\"`.", Required: true},
					"description":  schema.StringAttribute{MarkdownDescription: "Subnet description.", Optional: true},
				},
			},
			"initial_acl": schema.SingleNestedAttribute{
				MarkdownDescription: "The first network ACL, created alongside the VPC. Used at create time " +
					"only: it is never read back or reconciled, and editing it forces a new VPC.",
				Optional:      true,
				PlanModifiers: []planmodifier.Object{replaceIfPreviouslySet()},
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{MarkdownDescription: "ACL name.", Required: true},
					"rules": schema.ListNestedAttribute{
						MarkdownDescription: "Ordered ACL rules.",
						Optional:            true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"protocol":     schema.StringAttribute{MarkdownDescription: "Protocol, for example `TCP`.", Required: true},
								"action":       schema.StringAttribute{MarkdownDescription: "`Allow` or `Deny`.", Required: true},
								"source_cidr":  schema.StringAttribute{MarkdownDescription: "Source CIDR, for example `0.0.0.0/0`.", Required: true},
								"traffic_type": schema.StringAttribute{MarkdownDescription: "`Ingress` or `Egress`.", Required: true},
								"start_port":   schema.StringAttribute{MarkdownDescription: "First port of the range, as a string.", Optional: true},
								"end_port":     schema.StringAttribute{MarkdownDescription: "Last port of the range, as a string.", Optional: true},
								"icmp_type":    schema.StringAttribute{MarkdownDescription: "ICMP type, as a string.", Optional: true},
								"icmp_code":    schema.StringAttribute{MarkdownDescription: "ICMP code, as a string.", Optional: true},
							},
						},
					},
				},
			},

			"created_at": schema.StringAttribute{MarkdownDescription: "Creation timestamp.", Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"initial_subnet_id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the subnet provisioned alongside the VPC.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"initial_acl_id": schema.StringAttribute{
				MarkdownDescription: "Identifier of the ACL provisioned alongside the VPC. Pass this to a " +
					"`cloudraya_vpc_network`'s `network_acl_id`.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *vpcResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *client.Client, got %T.", req.ProviderData))
		return
	}
	r.client = c
}

func (r *vpcResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := optionalString(plan.ProjectID)
	if projectID == "" {
		projectID = r.client.ProjectID()
	}
	if projectID == "" {
		resp.Diagnostics.AddAttributeError(path.Root("project_id"), "Missing project",
			"Set `project_id` on the resource or `project_id` on the provider.")
		return
	}

	body := map[string]any{
		"project_id": projectID,
		"region_id":  plan.RegionID.ValueString(),
		"vpc": map[string]any{
			"name":         plan.Name.ValueString(),
			"ip_address":   plan.IPAddress.ValueString(),
			"network_size": plan.NetworkSize.ValueString(),
			"description":  optionalString(plan.Description),
		},
	}

	if subnet, diags := vpcSubnetBody(ctx, plan.InitialSubnet); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	} else if subnet != nil {
		body["subnet"] = subnet
	}

	if acl, diags := vpcACLBody(ctx, plan.InitialACL); diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	} else if acl != nil {
		body["acl"] = acl
	}

	var created vpcCreateAPI
	if err := r.client.Do(ctx, http.MethodPost, client.ServiceNetwork, "/v1/vpcs", body, &created); err != nil {
		resp.Diagnostics.AddError("Unable to create VPC", err.Error())
		return
	}
	vpc := created.resolve()
	if vpc.ID == "" {
		resp.Diagnostics.AddError("Unable to create VPC",
			"The API accepted the request but returned no VPC id, so the resource cannot be tracked. "+
				"Check the CloudRaya console for an orphaned VPC named "+plan.Name.ValueString()+".")
		return
	}

	// Create is asynchronous ("Processing create VPC") and the platform refuses
	// any further operation — including delete — until it settles. Returning
	// early would make an apply immediately followed by a destroy fail.
	plan.InitialSubnetID = stringOrNullValue(created.VPCNetworkID)
	plan.InitialACLID = stringOrNullValue(created.ACLID)

	settled, err := r.waitForActive(ctx, vpc.ID, 20*time.Minute)
	if err != nil {
		resp.Diagnostics.AddError("VPC did not finish provisioning", err.Error()+
			"\n\nThe VPC was created with id "+vpc.ID+" and is now tracked by Terraform; "+
			"re-run apply once the platform has settled.")
		// Still record it: the VPC exists, and losing the id here would orphan it.
		r.apply(vpc, &plan, projectID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	r.apply(settled, &plan, projectID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// waitForActive polls the VPC until it leaves its provisioning state.
func (r *vpcResource) waitForActive(ctx context.Context, id string, timeout time.Duration) (*vpcAPI, error) {
	deadline := time.Now().Add(timeout)
	const interval = 10 * time.Second

	for {
		var vpc vpcAPI
		if err := r.client.Do(ctx, http.MethodGet, client.ServiceNetwork, "/v1/vpcs/"+id, nil, &vpc); err != nil {
			return nil, err
		}
		if vpc.StatusText != nil && !strings.EqualFold(*vpc.StatusText, "Provisioning") {
			return &vpc, nil
		}
		if time.Now().After(deadline) {
			state := "unknown"
			if vpc.StatusText != nil {
				state = *vpc.StatusText
			}
			return nil, fmt.Errorf("timed out after %s waiting for VPC %s; last status was %q", timeout, id, state)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (r *vpcResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var vpc vpcAPI
	err := r.client.Do(ctx, http.MethodGet, client.ServiceNetwork,
		"/v1/vpcs/"+state.ID.ValueString(), nil, &vpc)
	if err != nil {
		// Deleted outside Terraform: drop it from state so the next plan recreates it.
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read VPC", err.Error())
		return
	}

	r.apply(&vpc, &state, optionalString(state.ProjectID))
	if state.InitialSubnetID.IsNull() || state.InitialACLID.IsNull() {
		if err := r.recoverInitialIDs(ctx, &vpc, &state); err != nil {
			resp.Diagnostics.AddWarning("Could not recover the VPC's initial subnet and ACL",
				err.Error()+"\n\ninitial_subnet_id and initial_acl_id stay unset.")
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// recoverInitialIDs finds the subnet and ACL created together with the VPC.
// Only the create call names them, so after an import they are otherwise lost
// and any configuration referencing initial_acl_id breaks. The create call
// provisions all three in one go, so they share the VPC's created_at; a subnet
// or ACL added later does not. If the initial one has since been deleted,
// nothing matches and the attribute stays unset rather than pointing at the
// wrong object.
func (r *vpcResource) recoverInitialIDs(ctx context.Context, vpc *vpcAPI, m *vpcModel) error {
	if vpc.CreatedAt == nil || *vpc.CreatedAt == "" {
		return fmt.Errorf("the VPC reports no created_at to match against")
	}
	created := *vpc.CreatedAt

	type item struct {
		ID        string `json:"id"`
		CreatedAt string `json:"created_at"`
	}
	earliestAt := func(items []item) string {
		id := ""
		for _, it := range items {
			if it.CreatedAt == created && (id == "" || it.ID < id) {
				id = it.ID
			}
		}
		return id
	}

	if m.InitialSubnetID.IsNull() {
		var nets struct {
			Networks []item `json:"networks"`
		}
		if err := r.client.Do(ctx, http.MethodGet, client.ServiceNetwork,
			"/v1/vpcs/"+vpc.ID+"/networks", nil, &nets); err != nil {
			return fmt.Errorf("listing subnets: %w", err)
		}
		if id := earliestAt(nets.Networks); id != "" {
			m.InitialSubnetID = types.StringValue(id)
		}
	}

	if m.InitialACLID.IsNull() {
		var acls struct {
			ACLs []item `json:"acls"`
		}
		if err := r.client.Do(ctx, http.MethodGet, client.ServiceNetwork,
			"/v1/acls?vpc_id="+vpc.ID, nil, &acls); err != nil {
			return fmt.Errorf("listing ACLs: %w", err)
		}
		if id := earliestAt(acls.ACLs); id != "" {
			m.InitialACLID = types.StringValue(id)
		}
	}
	return nil
}

func (r *vpcResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state vpcModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	// Every other mutable-looking attribute forces replacement, so a rename is
	// the only change that can reach this method.
	if !plan.Name.Equal(state.Name) {
		if err := r.client.Do(ctx, http.MethodPut, client.ServiceNetwork, "/v1/vpcs/"+id,
			map[string]any{"name": plan.Name.ValueString()}, nil); err != nil {
			resp.Diagnostics.AddError("Unable to rename VPC", err.Error())
			return
		}
	}

	var vpc vpcAPI
	if err := r.client.Do(ctx, http.MethodGet, client.ServiceNetwork, "/v1/vpcs/"+id, nil, &vpc); err != nil {
		resp.Diagnostics.AddError("Unable to read VPC after update", err.Error())
		return
	}

	r.apply(&vpc, &plan, optionalString(state.ProjectID))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Deleting a subnet is asynchronous, so a VPC destroyed straight after one of
	// its subnets is refused with "Please wait for the current process to be
	// finished". Terraform deletes children first by design, which makes that
	// collision the normal case rather than an edge case.
	err := retryWhileBusy(ctx, 10*time.Minute, func() error {
		return r.client.Do(ctx, http.MethodDelete, client.ServiceNetwork,
			"/v1/vpcs/"+state.ID.ValueString(),
			map[string]any{"force": state.ForceDestroy.ValueBool()}, nil)
	})
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete VPC", err.Error())
		return
	}
}

// retryWhileBusy re-runs op while the platform reports that an earlier
// operation on the same object is still settling.
func retryWhileBusy(ctx context.Context, timeout time.Duration, op func() error) error {
	deadline := time.Now().Add(timeout)
	const interval = 10 * time.Second

	for {
		err := op()
		if err == nil || !isBusy(err) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("gave up after %s: %w", timeout, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// isBusy matches platform responses meaning "an earlier state transition on
// this object hasn't settled yet, retry shortly" — a request-level rejection,
// not a real validation error. "has not been detached" shows up even after a
// detach call has already succeeded and the record reads virtual_machine_id:
// null, because the delete-time check lags the field that confirms it.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "wait for the current process") ||
		strings.Contains(msg, "has not been detached")
}

func (r *vpcResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	// force_destroy has no API representation; seed the default so the first plan
	// after an import does not show a diff for it.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("force_destroy"), false)...)
}

// apply copies API values onto the model. A field the API omits keeps whatever
// the plan or prior state held, so an unverified response name cannot silently
// blank a configured attribute. initial_subnet and initial_acl are create-time
// inputs and are deliberately left untouched.
func stringOrNullValue(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}

func (r *vpcResource) apply(vpc *vpcAPI, m *vpcModel, projectID string) {
	// Only the create call reports these; a read or import cannot recover them.
	if m.InitialSubnetID.IsUnknown() {
		m.InitialSubnetID = types.StringNull()
	}
	if m.InitialACLID.IsUnknown() {
		m.InitialACLID = types.StringNull()
	}

	m.ID = types.StringValue(vpc.ID)
	setIfPresent(&m.Name, vpc.Name)
	setIfPresent(&m.RegionID, vpc.RegionID)
	setIfPresent(&m.IPAddress, vpc.IPAddress)
	setOptionalIfPresent(&m.Description, vpc.Description)

	if vpc.NetworkSize != nil && vpc.NetworkSize.Value != "" {
		m.NetworkSize = types.StringValue(vpc.NetworkSize.Value)
	}
	// The detail response names these "region" and "network_address"; after an
	// import they are otherwise empty, and as replace-on-change attributes an
	// empty value would plan a destroy-and-recreate of the whole VPC.
	if vpc.Region != nil {
		fillIfEmpty(&m.RegionID, *vpc.Region)
	}
	if vpc.NetworkAddress != nil {
		if ip, size, ok := splitCIDR(*vpc.NetworkAddress); ok {
			fillIfEmpty(&m.IPAddress, ip)
			fillIfEmpty(&m.NetworkSize, size)
		}
	}

	if vpc.ProjectID != nil && *vpc.ProjectID != "" {
		m.ProjectID = types.StringValue(*vpc.ProjectID)
	} else {
		m.ProjectID = types.StringValue(projectID)
	}

	if vpc.CreatedAt != nil {
		m.CreatedAt = types.StringValue(*vpc.CreatedAt)
	} else if m.CreatedAt.IsUnknown() {
		m.CreatedAt = types.StringNull()
	}
}

func vpcSubnetBody(ctx context.Context, obj types.Object) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	if obj.IsNull() || obj.IsUnknown() {
		return nil, diags
	}
	var subnet vpcInitialSubnetModel
	diags.Append(obj.As(ctx, &subnet, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return nil, diags
	}
	return map[string]any{
		"name":         subnet.Name.ValueString(),
		"ip_address":   subnet.IPAddress.ValueString(),
		"network_size": subnet.NetworkSize.ValueString(),
		"description":  optionalString(subnet.Description),
	}, diags
}

func vpcACLBody(ctx context.Context, obj types.Object) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	if obj.IsNull() || obj.IsUnknown() {
		return nil, diags
	}
	var acl vpcInitialACLModel
	diags.Append(obj.As(ctx, &acl, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return nil, diags
	}

	rules := make([]map[string]any, 0)
	if !acl.Rules.IsNull() && !acl.Rules.IsUnknown() {
		var parsed []vpcACLRuleModel
		diags.Append(acl.Rules.ElementsAs(ctx, &parsed, false)...)
		if diags.HasError() {
			return nil, diags
		}
		for _, rule := range parsed {
			rules = append(rules, map[string]any{
				"protocol":     rule.Protocol.ValueString(),
				"action":       rule.Action.ValueString(),
				"source_cidr":  rule.SourceCIDR.ValueString(),
				"traffic_type": rule.TrafficType.ValueString(),
				"start_port":   optionalString(rule.StartPort),
				"end_port":     optionalString(rule.EndPort),
				"icmp_type":    optionalString(rule.ICMPType),
				"icmp_code":    optionalString(rule.ICMPCode),
			})
		}
	}

	return map[string]any{
		"name":  acl.Name.ValueString(),
		"rules": rules,
	}, diags
}

// setIfPresent overwrites a required attribute only when the API actually
// returned a value for it.
func setIfPresent(dst *types.String, v *string) {
	if v == nil || *v == "" {
		return
	}
	*dst = types.StringValue(*v)
}

// setOptionalIfPresent is setIfPresent for optional attributes: an empty string
// from the API is treated as "unset" when the configuration left it null, so an
// unconfigured attribute does not flip between null and "".
func setOptionalIfPresent(dst *types.String, v *string) {
	if v == nil {
		return
	}
	if *v == "" && (dst.IsNull() || dst.IsUnknown()) {
		*dst = types.StringNull()
		return
	}
	*dst = types.StringValue(*v)
}

// replaceIfPreviouslySet forces replacement when a create-time block changes,
// but not when state holds no value. That happens after an import, because the
// API never reports these blocks back; replacing then would destroy the VPC just
// to record what it was created with.
func replaceIfPreviouslySet() planmodifier.Object {
	return objectplanmodifier.RequiresReplaceIf(
		func(_ context.Context, req planmodifier.ObjectRequest, resp *objectplanmodifier.RequiresReplaceIfFuncResponse) {
			resp.RequiresReplace = !req.StateValue.IsNull()
		},
		"Changing this forces a new VPC, except when it is first recorded after an import.",
		"Changing this forces a new VPC, except when it is first recorded after an import.",
	)
}
