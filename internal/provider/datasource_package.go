package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/cloudraya/terraform-provider-cloudraya/internal/client"
)

var (
	_ datasource.DataSource              = (*packageDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*packageDataSource)(nil)
)

func NewPackageDataSource() datasource.DataSource { return &packageDataSource{} }

type packageDataSource struct {
	client *client.Client
}

type productLookupModel struct {
	Name       types.String `tfsdk:"name"`
	RegionID   types.String `tfsdk:"region_id"`
	ProjectID  types.String `tfsdk:"project_id"`
	ID         types.String `tfsdk:"id"`
	StatusText types.String `tfsdk:"status_text"`
}

func (d *packageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_package"
}

func (d *packageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a CloudRaya virtual machine package (size) by name, scoped to a " +
			"region. Fails with a specific error if the package exists but is not active in `region_id`, rather " +
			"than letting a VM create fail opaquely later.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "Package name, for example `Small-R2`.",
				Required:            true,
			},
			"region_id": schema.StringAttribute{
				MarkdownDescription: "Region the package must be active in.",
				Required:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Project to query under. Defaults to the provider's `project_id`.",
				Optional:            true,
				Computed:            true,
			},
			"id":          schema.StringAttribute{MarkdownDescription: "Package identifier (ULID), usable as a `package_id`.", Computed: true},
			"status_text": schema.StringAttribute{MarkdownDescription: "Package status.", Computed: true},
		},
	}
}

func (d *packageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *packageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	readProductLookup(ctx, d.client, req, resp, "vm")
}

// readProductLookup is shared by the package, template and vm_storage_package
// data sources: same model, same client call, only the product_type code and
// whether region_id is required differ.
func readProductLookup(ctx context.Context, c *client.Client, req datasource.ReadRequest, resp *datasource.ReadResponse, typeCode string) {
	var cfg productLookupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := cfg.ProjectID.ValueString()
	if projectID == "" {
		projectID = c.ProjectID()
	}
	if projectID == "" {
		resp.Diagnostics.AddAttributeError(path.Root("project_id"), "Missing project",
			"Set `project_id` on the data source or `project_id` on the provider.")
		return
	}

	p, err := findProduct(ctx, c, projectID, typeCode, cfg.Name.ValueString(), optionalString(cfg.RegionID))
	if err != nil {
		resp.Diagnostics.AddError(fmt.Sprintf("Unable to find %s product", typeCode), err.Error())
		return
	}

	cfg.ID = types.StringValue(p.ID)
	cfg.StatusText = types.StringValue(p.StatusText)
	cfg.ProjectID = types.StringValue(projectID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
