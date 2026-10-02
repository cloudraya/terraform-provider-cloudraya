package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/cloudraya/terraform-provider-cloudraya/internal/client"
)

var (
	_ resource.Resource                = (*vpcNetworkResource)(nil)
	_ resource.ResourceWithConfigure   = (*vpcNetworkResource)(nil)
	_ resource.ResourceWithImportState = (*vpcNetworkResource)(nil)
)

func NewVPCNetworkResource() resource.Resource { return &vpcNetworkResource{} }

type vpcNetworkResource struct {
	client *client.Client
}

type vpcNetworkModel struct {
	ID           types.String `tfsdk:"id"`
	VPCID        types.String `tfsdk:"vpc_id"`
	NetworkACLID types.String `tfsdk:"network_acl_id"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	IPAddress    types.String `tfsdk:"ip_address"`
	NetworkSize  types.String `tfsdk:"network_size"`

	// Computed
	NetworkAddress types.String `tfsdk:"network_address"`
	Gateway        types.String `tfsdk:"gateway"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// vpcNetworkAPI mirrors the "data" object returned by the VPC network endpoints.
// The detail response reports the CIDR as a single "network_address" and links
// its ACL under "network_acl", rather than echoing back the ip_address and
// network_size accepted on create. Pointers distinguish "absent" from "empty"
// so a field the API does not echo leaves the configured value in state alone.
type vpcNetworkAPI struct {
	ID             string      `json:"id"`
	VPCID          *string     `json:"vpc_id"`
	Name           *string     `json:"name"`
	Description    *string     `json:"description"`
	NetworkAddress *string     `json:"network_address"`
	Gateway        *string     `json:"gateway"`
	IPAddress      *string     `json:"ip_address"`
	NetworkSize    *flexString `json:"network_size"`
	CreatedAt      *string     `json:"created_at"`

	StatusText *string `json:"status_text"`

	NetworkACL *struct {
		ID string `json:"id"`
	} `json:"network_acl"`
}

// vpcNetworkCreateAPI decodes the create response, which answers
// {"vpc_network_id": "..."} rather than the network object. The embedded
// struct and nested "network" stay as fallbacks for other response shapes.
type vpcNetworkCreateAPI struct {
	vpcNetworkAPI
	Network      *vpcNetworkAPI `json:"network"`
	VPCNetworkID string         `json:"vpc_network_id"`
}

func (c *vpcNetworkCreateAPI) resolve() *vpcNetworkAPI {
	if c.VPCNetworkID != "" {
		return &vpcNetworkAPI{ID: c.VPCNetworkID}
	}
	if c.Network != nil && c.Network.ID != "" {
		return c.Network
	}
	return &c.vpcNetworkAPI
}

func (r *vpcNetworkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc_network"
}

func (r *vpcNetworkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A network (subnet) inside a CloudRaya VPC. Supports the full lifecycle: " +
			"create, read, in-place rename and description edits, and destroy.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Network identifier.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"vpc_id": schema.StringAttribute{
				MarkdownDescription: "VPC that owns this network. Changing this forces a new network.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Network name. Changing this renames the network in place.",
				Required:            true,
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description. Changing this updates in place.",
				Optional:            true,
			},
			"ip_address": schema.StringAttribute{
				MarkdownDescription: "Network address of the subnet, for example `10.20.1.0`. " +
					"Changing this forces a new network.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"network_size": schema.StringAttribute{
				MarkdownDescription: "CIDR prefix length of the subnet as a string, for example `\"24\"`. " +
					"Changing this forces a new network.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},

			"network_acl_id": schema.StringAttribute{
				MarkdownDescription: "Network ACL to attach the subnet to. CloudRaya requires a subnet to " +
					"have an ACL at creation time. Use the parent VPC's `initial_acl_id`, or any other ACL " +
					"in the same VPC. Changing this forces a new subnet.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},

			"network_address": schema.StringAttribute{MarkdownDescription: "Resolved CIDR, for example `10.50.33.0/24`.", Computed: true},
			"gateway":         schema.StringAttribute{MarkdownDescription: "Gateway address of the subnet.", Computed: true},
			"created_at":      schema.StringAttribute{MarkdownDescription: "Creation timestamp.", Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
	}
}

func (r *vpcNetworkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *vpcNetworkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcNetworkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The API requires either an inline acl or a reference to an existing one:
	// "The acl field is required when network acl id is not present."
	body := map[string]any{
		"name":           plan.Name.ValueString(),
		"description":    optionalString(plan.Description),
		"ip_address":     plan.IPAddress.ValueString(),
		"network_size":   plan.NetworkSize.ValueString(),
		"network_acl_id": plan.NetworkACLID.ValueString(),
	}

	vpcID := plan.VPCID.ValueString()
	var created vpcNetworkCreateAPI
	if err := r.client.Do(ctx, http.MethodPost, client.ServiceNetwork,
		"/v1/vpcs/"+vpcID+"/networks", body, &created); err != nil {
		resp.Diagnostics.AddError("Unable to create VPC network", err.Error())
		return
	}
	network := created.resolve()
	if network.ID == "" {
		resp.Diagnostics.AddError("Unable to create VPC network",
			"The API accepted the request but returned no network id, so the resource cannot be tracked. "+
				"Check VPC "+vpcID+" in the CloudRaya console for an orphaned network named "+
				plan.Name.ValueString()+".")
		return
	}

	// Create is asynchronous ("Processing create user VPC Network data...");
	// the subnet is not usable — or deletable — until it reports Active.
	settled, err := r.waitForActive(ctx, vpcID, network.ID, 15*time.Minute)
	if err != nil {
		resp.Diagnostics.AddError("VPC network did not finish provisioning", err.Error()+
			"\n\nThe network was created with id "+network.ID+" and is now tracked by Terraform.")
		r.apply(network, &plan, vpcID)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	r.apply(settled, &plan, vpcID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcNetworkResource) waitForActive(ctx context.Context, vpcID, id string, timeout time.Duration) (*vpcNetworkAPI, error) {
	deadline := time.Now().Add(timeout)
	const interval = 10 * time.Second

	for {
		var n vpcNetworkAPI
		err := r.client.Do(ctx, http.MethodGet, client.ServiceNetwork,
			"/v1/vpcs/"+vpcID+"/networks/"+id, nil, &n)
		if err != nil {
			return nil, err
		}
		if n.StatusText != nil && !strings.EqualFold(*n.StatusText, "Provisioning") &&
			!strings.EqualFold(*n.StatusText, "Processing") {
			return &n, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s waiting for VPC network %s", timeout, id)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (r *vpcNetworkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcNetworkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vpcID := state.VPCID.ValueString()
	var network vpcNetworkAPI
	err := r.client.Do(ctx, http.MethodGet, client.ServiceNetwork,
		"/v1/vpcs/"+vpcID+"/networks/"+state.ID.ValueString(), nil, &network)
	if err != nil {
		// Deleted outside Terraform — or the whole VPC was: drop it from state so
		// the next plan recreates it.
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read VPC network", err.Error())
		return
	}

	r.apply(&network, &state, vpcID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vpcNetworkResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state vpcNetworkModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vpcID := state.VPCID.ValueString()
	endpoint := "/v1/vpcs/" + vpcID + "/networks/" + state.ID.ValueString()

	// The endpoint replaces both fields, so send them together even when only one
	// changed — omitting the other would blank it.
	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) {
		body := map[string]any{
			"name":        plan.Name.ValueString(),
			"description": optionalString(plan.Description),
		}
		if err := r.client.Do(ctx, http.MethodPut, client.ServiceNetwork, endpoint, body, nil); err != nil {
			resp.Diagnostics.AddError("Unable to update VPC network", err.Error())
			return
		}
	}

	var network vpcNetworkAPI
	if err := r.client.Do(ctx, http.MethodGet, client.ServiceNetwork, endpoint, nil, &network); err != nil {
		resp.Diagnostics.AddError("Unable to read VPC network after update", err.Error())
		return
	}

	r.apply(&network, &plan, vpcID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcNetworkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcNetworkModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A rename/update just before delete (or a sibling subnet's own delete) can
	// still be settling, which the platform reports as "wait for the current
	// process to be finished" — the same collision retryWhileBusy handles for
	// the parent VPC's delete.
	err := retryWhileBusy(ctx, 10*time.Minute, func() error {
		return r.client.Do(ctx, http.MethodDelete, client.ServiceNetwork,
			"/v1/vpcs/"+state.VPCID.ValueString()+"/networks/"+state.ID.ValueString(), nil, nil)
	})
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete VPC network", err.Error())
		return
	}
}

// ImportState takes "vpc_id/network_id": the network id alone is not enough,
// because every read and write is addressed under its parent VPC.
func (r *vpcNetworkResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	vpcID, networkID, ok := strings.Cut(req.ID, "/")
	if !ok || vpcID == "" || networkID == "" || strings.Contains(networkID, "/") {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected \"vpc_id/network_id\", got %q.", req.ID))
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), vpcID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), networkID)...)
}

// apply copies API values onto the model. A field the API omits keeps whatever
// the plan or prior state held, so an unverified response name cannot silently
// blank a configured attribute.
func (r *vpcNetworkResource) apply(n *vpcNetworkAPI, m *vpcNetworkModel, vpcID string) {
	m.ID = types.StringValue(n.ID)
	setIfPresent(&m.Name, n.Name)
	setIfPresent(&m.IPAddress, n.IPAddress)
	setOptionalIfPresent(&m.Description, n.Description)

	if n.NetworkSize != nil && n.NetworkSize.Value != "" {
		m.NetworkSize = types.StringValue(n.NetworkSize.Value)
	}
	// Reported only as "network_address"; recovered so an import does not plan
	// a replacement.
	if n.NetworkAddress != nil {
		if ip, size, ok := splitCIDR(*n.NetworkAddress); ok {
			fillIfEmpty(&m.IPAddress, ip)
			fillIfEmpty(&m.NetworkSize, size)
		}
	}

	if n.VPCID != nil && *n.VPCID != "" {
		m.VPCID = types.StringValue(*n.VPCID)
	} else {
		m.VPCID = types.StringValue(vpcID)
	}

	if n.NetworkACL != nil && n.NetworkACL.ID != "" {
		m.NetworkACLID = types.StringValue(n.NetworkACL.ID)
	}

	m.NetworkAddress = stringOrNull(n.NetworkAddress, m.NetworkAddress)
	m.Gateway = stringOrNull(n.Gateway, m.Gateway)

	if n.CreatedAt != nil {
		m.CreatedAt = types.StringValue(*n.CreatedAt)
	} else if m.CreatedAt.IsUnknown() {
		m.CreatedAt = types.StringNull()
	}
}

// stringOrNull keeps an unknown computed attribute from surviving into state
// when the API does not return the field.
func stringOrNull(v *string, current types.String) types.String {
	if v != nil {
		return types.StringValue(*v)
	}
	if current.IsUnknown() {
		return types.StringNull()
	}
	return current
}
