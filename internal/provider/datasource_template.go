package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/cloudraya/terraform-provider-cloudraya/internal/client"
)

var (
	_ datasource.DataSource              = (*templateDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*templateDataSource)(nil)
)

func NewTemplateDataSource() datasource.DataSource { return &templateDataSource{} }

type templateDataSource struct {
	client *client.Client
}

func (d *templateDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_template"
}

func (d *templateDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a CloudRaya OS template by name, scoped to a region — templates " +
			"are offered per region, so the same name can exist in one region and not another. Backed " +
			"by the product_2 catalog — see the provider README for its limitations.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "Template name, for example `Ubuntu 22.04 v05.22`.",
				Required:            true,
			},
			"region_id": schema.StringAttribute{
				MarkdownDescription: "Region the template must be active in.",
				Required:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Project to query under. Defaults to the provider's `project_id`.",
				Optional:            true,
				Computed:            true,
			},
			"id":          schema.StringAttribute{MarkdownDescription: "Template identifier (ULID), usable as a `template_id`.", Computed: true},
			"status_text": schema.StringAttribute{MarkdownDescription: "Template status.", Computed: true},
		},
	}
}

func (d *templateDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *templateDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	readProductLookup(ctx, d.client, req, resp, "template")
}
