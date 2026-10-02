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
	_ datasource.DataSource              = (*regionDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*regionDataSource)(nil)
)

func NewRegionsDataSource() datasource.DataSource { return &regionDataSource{} }

type regionDataSource struct {
	client *client.Client
}

type regionDataSourceModel struct {
	Name       types.String `tfsdk:"name"`
	ProjectID  types.String `tfsdk:"project_id"`
	ID         types.String `tfsdk:"id"`
	Type       types.String `tfsdk:"type"`
	StatusText types.String `tfsdk:"status_text"`
}

// regionAPI mirrors one entry of GET /v1/regions on the product catalog.
type regionAPI struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	IsActive    int64  `json:"is_active"`
	StatusText  string `json:"status_text"`
}

// regionListAPI: the response is {"regions": [...]}, not a bare array.
type regionListAPI struct {
	Regions []regionAPI `json:"regions"`
}

func (d *regionDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_region"
}

func (d *regionDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a CloudRaya region by its display name.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "Region display name, for example `Jakarta`.",
				Required:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Project to query under. Defaults to the provider's `project_id`.",
				Optional:            true,
				Computed:            true,
			},
			"id":          schema.StringAttribute{MarkdownDescription: "Region identifier (ULID).", Computed: true},
			"type":        schema.StringAttribute{MarkdownDescription: "Region backend, for example `cloudstack`, `s3` or `cir`.", Computed: true},
			"status_text": schema.StringAttribute{MarkdownDescription: "Region status.", Computed: true},
		},
	}
}

func (d *regionDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *regionDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg regionDataSourceModel
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

	var list regionListAPI
	if err := d.client.DoCatalog(ctx, http.MethodGet,
		"/v1/regions?project_id="+projectID, nil, &list); err != nil {
		resp.Diagnostics.AddError("Unable to list regions", err.Error())
		return
	}

	name := cfg.Name.ValueString()
	var matches []regionAPI
	for _, r := range list.Regions {
		if r.DisplayName == name {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Region not found",
			fmt.Sprintf("No region named %q was found for this project.", name))
		return
	case 1:
		// fall through
	default:
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Ambiguous region name",
			fmt.Sprintf("%d regions are named %q; region names are expected to be unique.", len(matches), name))
		return
	}

	r := matches[0]
	cfg.ID = types.StringValue(r.ID)
	cfg.Type = types.StringValue(r.Type)
	cfg.StatusText = types.StringValue(r.StatusText)
	cfg.ProjectID = types.StringValue(projectID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
