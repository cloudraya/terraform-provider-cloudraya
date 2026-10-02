package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"

	"github.com/cloudraya/terraform-provider-cloudraya/internal/client"
)

var (
	_ datasource.DataSource              = (*vmStoragePackageDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*vmStoragePackageDataSource)(nil)
)

func NewVMStoragePackageDataSource() datasource.DataSource { return &vmStoragePackageDataSource{} }

type vmStoragePackageDataSource struct {
	client *client.Client
}

func (d *vmStoragePackageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vm_storage_package"
}

func (d *vmStoragePackageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a CloudRaya block-storage (data disk) package by name. Backed by " +
			"the product_2 catalog — see the provider README for its limitations.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				MarkdownDescription: "Storage package name, for example `new-storage`.",
				Required:            true,
			},
			"region_id": schema.StringAttribute{
				MarkdownDescription: "Region the package must be active in. Optional: storage packages " +
					"are commonly offered everywhere, so omit this to match by name alone.",
				Optional: true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Project to query under. Defaults to the provider's `project_id`.",
				Optional:            true,
				Computed:            true,
			},
			"id":          schema.StringAttribute{MarkdownDescription: "Package identifier (ULID), usable as a volume `product_id`.", Computed: true},
			"status_text": schema.StringAttribute{MarkdownDescription: "Package status.", Computed: true},
		},
	}
}

func (d *vmStoragePackageDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *vmStoragePackageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	readProductLookup(ctx, d.client, req, resp, "storage")
}
