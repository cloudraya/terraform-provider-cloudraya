package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/cloudraya/terraform-provider-cloudraya/internal/client"
)

var _ provider.Provider = (*cloudrayaProvider)(nil)

type cloudrayaProvider struct {
	version string
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &cloudrayaProvider{version: version}
	}
}

type providerModel struct {
	Email     types.String `tfsdk:"email"`
	Password  types.String `tfsdk:"password"`
	BaseURL   types.String `tfsdk:"base_url"`
	ProjectID types.String `tfsdk:"project_id"`
}

func (p *cloudrayaProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "cloudraya"
	resp.Version = p.version
}

func (p *cloudrayaProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage CloudRaya infrastructure. The provider exchanges your CloudRaya " +
			"account e-mail and password for a bearer token, refreshes it before it expires, and " +
			"sends it on every request.",
		Attributes: map[string]schema.Attribute{
			"email": schema.StringAttribute{
				MarkdownDescription: "CloudRaya account e-mail. May also be set with `CLOUDRAYA_EMAIL`.",
				Optional:            true,
			},
			"password": schema.StringAttribute{
				MarkdownDescription: "CloudRaya account password. May also be set with `CLOUDRAYA_PASSWORD`.",
				Optional:            true,
				Sensitive:           true,
			},
			"base_url": schema.StringAttribute{
				MarkdownDescription: "API host. Defaults to `" + client.DefaultBaseURL + "`. " +
					"May also be set with `CLOUDRAYA_BASE_URL`.",
				Optional: true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Default project for resources that do not set one. " +
					"May also be set with `CLOUDRAYA_PROJECT_ID`.",
				Optional: true,
			},
		},
	}
}

func (p *cloudrayaProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Unknown values mean another resource must be applied first; the framework
	// re-invokes Configure once they are resolved.
	if cfg.Email.IsUnknown() || cfg.Password.IsUnknown() {
		return
	}

	email := firstNonEmpty(cfg.Email.ValueString(), os.Getenv("CLOUDRAYA_EMAIL"))
	password := firstNonEmpty(cfg.Password.ValueString(), os.Getenv("CLOUDRAYA_PASSWORD"))
	baseURL := firstNonEmpty(cfg.BaseURL.ValueString(), os.Getenv("CLOUDRAYA_BASE_URL"))
	projectID := firstNonEmpty(cfg.ProjectID.ValueString(), os.Getenv("CLOUDRAYA_PROJECT_ID"))

	if email == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("email"),
			"Missing CloudRaya e-mail",
			"Set the provider's `email` argument or the CLOUDRAYA_EMAIL environment variable.",
		)
	}
	if password == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("password"),
			"Missing CloudRaya password",
			"Set the provider's `password` argument or the CLOUDRAYA_PASSWORD environment variable.",
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	c, err := client.New(client.Config{
		Email:     email,
		Password:  password,
		BaseURL:   baseURL,
		ProjectID: projectID,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create CloudRaya API client", err.Error())
		return
	}

	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *cloudrayaProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewVirtualMachineResource,
		NewVolumeResource,
		NewVPCResource,
		NewVPCNetworkResource,
		NewSSHKeypairResource,
	}
}

func (p *cloudrayaProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewRegionsDataSource,
		NewPackageDataSource,
		NewTemplateDataSource,
		NewVMStoragePackageDataSource,
		NewVPCNetworkDataSource,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
