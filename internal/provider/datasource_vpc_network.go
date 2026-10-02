package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/cloudraya/terraform-provider-cloudraya/internal/client"
)

var (
	_ datasource.DataSource              = (*vpcNetworkDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*vpcNetworkDataSource)(nil)
)

func NewVPCNetworkDataSource() datasource.DataSource { return &vpcNetworkDataSource{} }

type vpcNetworkDataSource struct {
	client *client.Client
}

type vpcNetworkDataSourceModel struct {
	Name           types.String `tfsdk:"name"`
	ProjectID      types.String `tfsdk:"project_id"`
	ID             types.String `tfsdk:"id"`
	VPCID          types.String `tfsdk:"vpc_id"`
	NetworkAddress types.String `tfsdk:"network_address"`
}

// allNetworksAPI mirrors one entry of GET /v1/vpcs/all-networks — a flattened
// list of every subnet across every VPC in the project, which avoids having
// to list VPCs and then list each one's subnets to find one by name.
type allNetworksAPI struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	VPCID          *string `json:"vpc_id"`
	NetworkAddress *string `json:"network_address"`
}

type allNetworksListAPI struct {
	Networks []allNetworksAPI `json:"networks"`
}

func (d *vpcNetworkDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc_network"
}

func (d *vpcNetworkDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up an existing CloudRaya VPC subnet by name, across every VPC in " +
			"the project. To create a subnet instead, use the `cloudraya_vpc_network` resource.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "Subnet name, for example `default-vpcnet-042258`.",
				Required:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Project to query under. Defaults to the provider's `project_id`.",
				Optional:            true,
				Computed:            true,
			},
			"id":              schema.StringAttribute{MarkdownDescription: "Subnet identifier (ULID), usable as a `network_id`.", Computed: true},
			"vpc_id":          schema.StringAttribute{MarkdownDescription: "VPC the subnet belongs to.", Computed: true},
			"network_address": schema.StringAttribute{MarkdownDescription: "Resolved CIDR, for example `10.20.0.0/24`.", Computed: true},
		},
	}
}

func (d *vpcNetworkDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("Expected *client.Client, got %T.", req.ProviderData))
		return
	}
	d.client = c
}

func (d *vpcNetworkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg vpcNetworkDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := cfg.ProjectID.ValueString()
	if projectID == "" {
		projectID = d.client.ProjectID()
	}
	if projectID == "" {
		resp.Diagnostics.AddAttributeError(path.Root("project_id"), "Missing project",
			"Set `project_id` on the data source or `project_id` on the provider.")
		return
	}

	var list allNetworksListAPI
	if err := d.client.Do(ctx, http.MethodGet, client.ServiceNetwork,
		"/v1/vpcs/all-networks?project_id="+projectID, nil, &list); err != nil {
		resp.Diagnostics.AddError("Unable to list VPC networks", err.Error())
		return
	}

	name := cfg.Name.ValueString()
	var matches []allNetworksAPI
	for _, n := range list.Networks {
		if n.Name == name {
			matches = append(matches, n)
		}
	}
	switch len(matches) {
	case 0:
		resp.Diagnostics.AddAttributeError(path.Root("name"), "VPC network not found",
			fmt.Sprintf("No subnet named %q was found in this project.", name))
		return
	case 1:
		// fall through
	default:
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Ambiguous subnet name",
			fmt.Sprintf("%d subnets are named %q; subnet names are expected to be unique within a project.", len(matches), name))
		return
	}

	n := matches[0]
	cfg.ID = types.StringValue(n.ID)
	cfg.ProjectID = types.StringValue(projectID)
	if n.VPCID != nil {
		cfg.VPCID = types.StringValue(*n.VPCID)
	} else {
		cfg.VPCID = types.StringValue("")
	}
	if n.NetworkAddress != nil {
		cfg.NetworkAddress = types.StringValue(*n.NetworkAddress)
	} else {
		cfg.NetworkAddress = types.StringValue("")
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
