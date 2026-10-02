package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/cloudraya/terraform-provider-cloudraya/internal/client"
)

var (
	_ resource.Resource                = (*virtualMachineResource)(nil)
	_ resource.ResourceWithConfigure   = (*virtualMachineResource)(nil)
	_ resource.ResourceWithImportState = (*virtualMachineResource)(nil)
)

func NewVirtualMachineResource() resource.Resource { return &virtualMachineResource{} }

type virtualMachineResource struct {
	client *client.Client
}

type virtualMachineModel struct {
	ID             types.String `tfsdk:"id"`
	Hostname       types.String `tfsdk:"hostname"`
	RegionID       types.String `tfsdk:"region_id"`
	PackageID      types.String `tfsdk:"package_id"`
	TemplateID     types.String `tfsdk:"template_id"`
	NetworkID      types.String `tfsdk:"network_id"`
	SSHKeypairIDs  types.List   `tfsdk:"ssh_keypair_ids"`
	ProjectID      types.String `tfsdk:"project_id"`
	Note           types.String `tfsdk:"note"`
	KeepIPOnDelete types.Bool   `tfsdk:"keep_ip_on_delete"`

	// Computed
	CPUNumber      types.Int64  `tfsdk:"cpu_number"`
	RAMGB          types.Int64  `tfsdk:"ram_gb"`
	RootDiskSizeGB types.Int64  `tfsdk:"rootdisk_size_gb"`
	TemplateName   types.String `tfsdk:"template_name"`
	State          types.String `tfsdk:"state"`
	StatusLabel    types.String `tfsdk:"status_label"`
	CreatedAt      types.String `tfsdk:"created_at"`
}

// vmAPI mirrors the "data" object returned by the VM endpoints. Verified
// against a live response: status and state are objects, not scalars.
type vmAPI struct {
	ID             string  `json:"id"`
	Hostname       string  `json:"hostname"`
	ProjectID      string  `json:"project_id"`
	RegionID       string  `json:"region_id"`
	VPCID          string  `json:"vpc_id"`
	NetworkID      string  `json:"network_id"`
	PackageID      string  `json:"package_id"`
	TemplateID     string  `json:"template_id"`
	TemplateName   string  `json:"template_name"`
	TemplateType   string  `json:"template_type"`
	Note           *string `json:"note"`
	RAMGB          int64   `json:"ram_gb"`
	RootDiskSizeGB int64   `json:"rootdisk_size_gb"`
	CPUNumber      int64   `json:"cpu_number"`
	CreatedAt      string  `json:"created_at"`

	Status struct {
		Value int64  `json:"value"`
		Label string `json:"label"`
	} `json:"status"`
	State struct {
		Value      string `json:"value"`
		Label      string `json:"label"`
		Actionable bool   `json:"actionable"`
	} `json:"state"`
}

func (r *virtualMachineResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_virtual_machine"
}

func (r *virtualMachineResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A CloudRaya virtual machine. Supports the full lifecycle: deploy, read, " +
			"in-place updates (rename, resize, SSH keys, network) and destroy.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "VM identifier.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"hostname": schema.StringAttribute{
				MarkdownDescription: "Hostname. Changing this renames the VM in place.",
				Required:            true,
			},
			"region_id": schema.StringAttribute{
				MarkdownDescription: "Region to deploy into. Changing this forces a new VM.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"package_id": schema.StringAttribute{
				MarkdownDescription: "Compute package (CPU/RAM/disk). Changing this scales the VM in place.",
				Required:            true,
			},
			"template_id": schema.StringAttribute{
				MarkdownDescription: "OS template. Changing this forces a new VM — reinstalling in place " +
					"would destroy the root disk contents without Terraform being able to represent that.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"network_id": schema.StringAttribute{
				MarkdownDescription: "Network to attach. Changing this moves the VM in place.",
				Required:            true,
			},
			"ssh_keypair_ids": schema.ListAttribute{
				MarkdownDescription: "SSH keypair IDs authorised on the VM. Changing this updates in place.",
				ElementType:         types.StringType,
				Optional:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Project that owns the VM. Defaults to the provider's `project_id`. " +
					"Changing this forces a new VM.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
			"note": schema.StringAttribute{
				MarkdownDescription: "Free-form note. Changing this updates in place.",
				Optional:            true,
			},
			"keep_ip_on_delete": schema.BoolAttribute{
				MarkdownDescription: "Retain the public IP when the VM is destroyed. Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},

			"cpu_number":       schema.Int64Attribute{MarkdownDescription: "vCPU count, from the package.", Computed: true},
			"ram_gb":           schema.Int64Attribute{MarkdownDescription: "RAM in GB, from the package.", Computed: true},
			"rootdisk_size_gb": schema.Int64Attribute{MarkdownDescription: "Root disk size in GB.", Computed: true},
			"template_name":    schema.StringAttribute{MarkdownDescription: "Resolved OS template name.", Computed: true},
			"state":            schema.StringAttribute{MarkdownDescription: "Power state reported by the platform.", Computed: true},
			"status_label":     schema.StringAttribute{MarkdownDescription: "Provisioning status label.", Computed: true},
			"created_at":       schema.StringAttribute{MarkdownDescription: "Creation timestamp.", Computed: true},
		},
	}
}

func (r *virtualMachineResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *virtualMachineResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan virtualMachineModel
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

	keyIDs, diags := stringListValues(ctx, plan.SSHKeypairIDs)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// ssh_keypair_id must be an array on deploy, even for zero or one key; a
	// bare string is rejected with "The ssh keypair id field must be an array."
	if keyIDs == nil {
		keyIDs = []string{}
	}

	body := map[string]any{
		"hostname":       plan.Hostname.ValueString(),
		"region_id":      plan.RegionID.ValueString(),
		"package_id":     plan.PackageID.ValueString(),
		"template_id":    plan.TemplateID.ValueString(),
		"network_id":     plan.NetworkID.ValueString(),
		"ssh_keypair_id": keyIDs,
		"note":           plan.Note.ValueString(),
		"project_id":     projectID,
	}

	var created vmAPI
	if err := r.client.Do(ctx, http.MethodPost, client.ServiceVM, "/v1/virtual-machines", body, &created); err != nil {
		resp.Diagnostics.AddError("Unable to deploy virtual machine", err.Error())
		return
	}
	if created.ID == "" {
		resp.Diagnostics.AddError("Unable to deploy virtual machine",
			"The API accepted the request but returned no VM id, so the resource cannot be tracked. "+
				"Check the CloudRaya console for an orphaned VM named "+plan.Hostname.ValueString()+".")
		return
	}

	// Deploy is asynchronous; wait for the VM to leave its transitional state so
	// the computed attributes we save are the real ones.
	vm, err := r.waitForReady(ctx, created.ID, 30*time.Minute)
	if err != nil {
		resp.Diagnostics.AddError("Virtual machine did not become ready", err.Error())
		return
	}

	r.applyComputed(vm, &plan, projectID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *virtualMachineResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state virtualMachineModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var vm vmAPI
	err := r.client.Do(ctx, http.MethodGet, client.ServiceVM,
		"/v1/virtual-machines/"+state.ID.ValueString(), nil, &vm)
	if err != nil {
		// Deleted outside Terraform: drop it from state so the next plan recreates it.
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read virtual machine", err.Error())
		return
	}

	r.applyAll(&vm, &state, state.ProjectID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update routes each changed attribute to its own endpoint: CloudRaya has no
// single "update VM" call, so a plan touching several fields becomes several
// requests.
func (r *virtualMachineResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state virtualMachineModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	base := "/v1/virtual-machines/" + id

	if !plan.Hostname.Equal(state.Hostname) {
		if err := r.client.Do(ctx, http.MethodPatch, client.ServiceVM, base+"/rename",
			map[string]any{"hostname": plan.Hostname.ValueString()}, nil); err != nil {
			resp.Diagnostics.AddError("Unable to rename virtual machine", err.Error())
			return
		}
	}

	if !plan.PackageID.Equal(state.PackageID) {
		if err := r.client.Do(ctx, http.MethodPatch, client.ServiceVM, base+"/change-package",
			map[string]any{"package_id": plan.PackageID.ValueString()}, nil); err != nil {
			resp.Diagnostics.AddError("Unable to change virtual machine package", err.Error())
			return
		}
	}

	if !plan.NetworkID.Equal(state.NetworkID) {
		if err := r.client.Do(ctx, http.MethodPatch, client.ServiceVM, base+"/change-network",
			map[string]any{"network_id": plan.NetworkID.ValueString()}, nil); err != nil {
			resp.Diagnostics.AddError("Unable to change virtual machine network", err.Error())
			return
		}
	}

	if !plan.SSHKeypairIDs.Equal(state.SSHKeypairIDs) {
		keyIDs, diags := stringListValues(ctx, plan.SSHKeypairIDs)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		if err := r.client.Do(ctx, http.MethodPatch, client.ServiceVM, base+"/change-sshkey",
			map[string]any{"ssh_keypair_id": keyIDs}, nil); err != nil {
			resp.Diagnostics.AddError("Unable to change virtual machine SSH keypairs", err.Error())
			return
		}
	}

	if !plan.Note.Equal(state.Note) {
		if err := r.client.Do(ctx, http.MethodPatch, client.ServiceVM, base+"/detail",
			map[string]any{"note": plan.Note.ValueString()}, nil); err != nil {
			resp.Diagnostics.AddError("Unable to update virtual machine note", err.Error())
			return
		}
	}

	// Re-read so computed attributes (vCPU/RAM after a resize) reflect reality
	// rather than the pre-update values carried over from state.
	vm, err := r.waitForReady(ctx, id, 20*time.Minute)
	if err != nil {
		resp.Diagnostics.AddError("Virtual machine did not settle after update", err.Error())
		return
	}
	r.applyComputed(vm, &plan, state.ProjectID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *virtualMachineResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state virtualMachineModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.Do(ctx, http.MethodDelete, client.ServiceVM,
		"/v1/virtual-machines/"+state.ID.ValueString()+"/destroy",
		map[string]any{"keep_ip": state.KeepIPOnDelete.ValueBool()}, nil)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to destroy virtual machine", err.Error())
		return
	}
}

func (r *virtualMachineResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// waitForReady polls until the VM leaves its transitional state. Deploy and
// resize are asynchronous, so returning immediately would save half-populated
// computed attributes.
func (r *virtualMachineResource) waitForReady(ctx context.Context, id string, timeout time.Duration) (*vmAPI, error) {
	deadline := time.Now().Add(timeout)
	const interval = 10 * time.Second

	// Deploy returns 200 with an id before the record is necessarily readable,
	// so a 404 in the first moments is treated as not-yet-visible. Past this
	// window a 404 means the platform discarded the deployment.
	visibilityGrace := time.Now().Add(90 * time.Second)
	seen := false

	for {
		var vm vmAPI
		err := r.client.Do(ctx, http.MethodGet, client.ServiceVM, "/v1/virtual-machines/"+id, nil, &vm)
		if err != nil {
			if client.IsNotFound(err) {
				if !seen && time.Now().Before(visibilityGrace) {
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-time.After(interval):
					}
					continue
				}
				return nil, fmt.Errorf("the platform accepted the deployment (id %s) but the virtual machine "+
					"no longer exists: it was rolled back during provisioning. This is a platform-side failure "+
					"rather than a configuration error — check the region's capacity and the account's virtual "+
					"machine quota", id)
			}
			return nil, err
		}
		seen = true
		switch vm.State.Value {
		case "running", "stopped":
			return &vm, nil
		case "error", "failed":
			return nil, fmt.Errorf("virtual machine %s entered state %q (%s)", id, vm.State.Value, vm.Status.Label)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s waiting for virtual machine %s; last state was %q",
				timeout, id, vm.State.Value)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// applyComputed fills only the attributes Terraform treats as computed.
//
// After create and update the configured attributes must keep their planned
// values: the framework rejects a state whose configured attributes differ from
// the plan ("Provider produced inconsistent result after apply"), and CloudRaya
// applies renames and resizes asynchronously, so echoing the API's values back
// would frequently do exactly that.
func (r *virtualMachineResource) applyComputed(vm *vmAPI, m *virtualMachineModel, projectID string) {
	m.ID = types.StringValue(vm.ID)

	if vm.ProjectID != "" {
		m.ProjectID = types.StringValue(vm.ProjectID)
	} else {
		m.ProjectID = types.StringValue(projectID)
	}

	m.CPUNumber = types.Int64Value(vm.CPUNumber)
	m.RAMGB = types.Int64Value(vm.RAMGB)
	m.RootDiskSizeGB = types.Int64Value(vm.RootDiskSizeGB)
	m.TemplateName = types.StringValue(vm.TemplateName)
	m.State = types.StringValue(vm.State.Value)
	m.StatusLabel = types.StringValue(vm.Status.Label)
	m.CreatedAt = types.StringValue(vm.CreatedAt)
}

// applyAll additionally refreshes the configured attributes from the API. Only
// Read uses it, where detecting drift is the whole point.
func (r *virtualMachineResource) applyAll(vm *vmAPI, m *virtualMachineModel, projectID string) {
	r.applyComputed(vm, m, projectID)

	m.Hostname = types.StringValue(vm.Hostname)
	m.RegionID = types.StringValue(vm.RegionID)
	m.PackageID = types.StringValue(vm.PackageID)
	m.TemplateID = types.StringValue(vm.TemplateID)
	m.NetworkID = types.StringValue(vm.NetworkID)
}
