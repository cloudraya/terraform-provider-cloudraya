package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/cloudraya/terraform-provider-cloudraya/internal/client"
)

var (
	_ resource.Resource                = (*sshKeypairResource)(nil)
	_ resource.ResourceWithConfigure   = (*sshKeypairResource)(nil)
	_ resource.ResourceWithImportState = (*sshKeypairResource)(nil)
)

func NewSSHKeypairResource() resource.Resource { return &sshKeypairResource{} }

type sshKeypairResource struct {
	client *client.Client
}

type sshKeypairModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	PublicKey types.String `tfsdk:"public_key"`
	ProjectID types.String `tfsdk:"project_id"`

	// Computed
	UserID    types.String `tfsdk:"user_id"`
	CreatedAt types.String `tfsdk:"created_at"`
}

// sshKeypairAPI mirrors the "data" object returned by the ssh-keypair endpoints.
type sshKeypairAPI struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	CreatedAt string `json:"created_at"`
}

func (r *sshKeypairResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_keypair"
}

func (r *sshKeypairResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A CloudRaya SSH keypair. The API exposes no update call, so every " +
			"configurable attribute forces a new keypair.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "SSH keypair identifier.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Keypair name. Changing this forces a new keypair.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"public_key": schema.StringAttribute{
				MarkdownDescription: "OpenSSH-format public key. Changing this forces a new keypair.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Project that owns the keypair. Defaults to the provider's `project_id`. " +
					"Changing this forces a new keypair.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},

			"user_id":    schema.StringAttribute{MarkdownDescription: "Owning user.", Computed: true},
			"created_at": schema.StringAttribute{MarkdownDescription: "Creation timestamp.", Computed: true},
		},
	}
}

func (r *sshKeypairResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *sshKeypairResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sshKeypairModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	projectID := plan.ProjectID.ValueString()
	if projectID == "" {
		projectID = r.client.ProjectID()
	}
	if projectID == "" {
		resp.Diagnostics.AddAttributeError(path.Root("project_id"), "Missing project",
			"Set `project_id` on the resource or `project_id` on the provider.")
		return
	}

	body := map[string]any{
		"name":       plan.Name.ValueString(),
		"public_key": plan.PublicKey.ValueString(),
	}

	var created sshKeypairAPI
	if err := r.client.Do(ctx, http.MethodPost, client.ServiceCore, "/v1/ssh-keypairs", body, &created); err != nil {
		resp.Diagnostics.AddError("Unable to create SSH keypair", err.Error())
		return
	}
	if created.ID == "" {
		resp.Diagnostics.AddError("Unable to create SSH keypair",
			"The API accepted the request but returned no keypair id, so the resource cannot be tracked. "+
				"Check the CloudRaya console for an orphaned keypair named "+plan.Name.ValueString()+".")
		return
	}

	r.applyComputed(&created, &plan, projectID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *sshKeypairResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sshKeypairModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var key sshKeypairAPI
	err := r.client.Do(ctx, http.MethodGet, client.ServiceCore,
		"/v1/ssh-keypairs/"+state.ID.ValueString(), nil, &key)
	if err != nil {
		// Deleted outside Terraform: drop it from state so the next plan recreates it.
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read SSH keypair", err.Error())
		return
	}

	r.applyAll(&key, &state, state.ProjectID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update can only be reached if a configurable attribute lost its
// RequiresReplace plan modifier: CloudRaya has no update endpoint, so there is
// nothing to send. Erroring here surfaces that provider bug instead of letting
// Terraform report a successful apply that changed nothing on the platform.
func (r *sshKeypairResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("SSH keypairs cannot be updated in place",
		"CloudRaya exposes no update endpoint for SSH keypairs, so every configurable attribute "+
			"is marked RequiresReplace and Terraform should never call Update. Reaching this point "+
			"indicates a provider bug — a missing RequiresReplace plan modifier. Please report it.")
}

func (r *sshKeypairResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sshKeypairModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Do(ctx, http.MethodDelete, client.ServiceCore,
		"/v1/ssh-keypairs/"+state.ID.ValueString(), nil, nil)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to delete SSH keypair", err.Error())
		return
	}
}

func (r *sshKeypairResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// applyComputed fills only the computed attributes.
//
// Create must leave name and public_key at their configured values. CloudRaya
// normalises the key it stores — it pads the base64 and rewrites the trailing
// comment to the keypair's name — so echoing the stored version back trips the
// framework's "inconsistent result after apply" check.
func (r *sshKeypairResource) applyComputed(key *sshKeypairAPI, m *sshKeypairModel, projectID string) {
	m.ID = types.StringValue(key.ID)

	if key.ProjectID != "" {
		m.ProjectID = types.StringValue(key.ProjectID)
	} else {
		m.ProjectID = types.StringValue(projectID)
	}

	m.UserID = types.StringValue(key.UserID)
	m.CreatedAt = types.StringValue(key.CreatedAt)
}

// applyAll also refreshes configured attributes; only Read uses it.
//
// public_key is deliberately left alone even here. Because the stored value
// never matches what was configured, copying it into state would show drift on
// every plan and force an endless replace. The cost is that an out-of-band key
// change goes undetected; the alternative is a resource that never converges.
func (r *sshKeypairResource) applyAll(key *sshKeypairAPI, m *sshKeypairModel, projectID string) {
	r.applyComputed(key, m, projectID)
	m.Name = types.StringValue(key.Name)
}
