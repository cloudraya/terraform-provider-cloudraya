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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/cloudraya/terraform-provider-cloudraya/internal/client"
)

var (
	_ resource.Resource                = (*volumeResource)(nil)
	_ resource.ResourceWithConfigure   = (*volumeResource)(nil)
	_ resource.ResourceWithImportState = (*volumeResource)(nil)
)

func NewVolumeResource() resource.Resource { return &volumeResource{} }

type volumeResource struct {
	client *client.Client
}

type volumeModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	RegionID         types.String `tfsdk:"region_id"`
	ProductID        types.String `tfsdk:"product_id"`
	VirtualMachineID types.String `tfsdk:"virtual_machine_id"`
	ProjectID        types.String `tfsdk:"project_id"`
	ForceDestroy     types.Bool   `tfsdk:"force_destroy"`

	// Computed
	DiskSizeGB  types.String `tfsdk:"disk_size_gb"`
	VolumeType  types.String `tfsdk:"volume_type"`
	StatusLabel types.String `tfsdk:"status_label"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

// volumeAPI mirrors the "data" object returned by the volume endpoints,
// verified against a live response. Two shapes are worth noting: `status` is an
// object rather than a scalar, and `disk_size` is a decimal *string*
// ("10.000"), so it is carried through as a string rather than being parsed
// into a number the API never promised.
type volumeAPI struct {
	ID               string  `json:"id"`
	Name             *string `json:"name"`
	RegionID         *string `json:"region_id"`
	ProjectID        *string `json:"project_id"`
	ProductID        *string `json:"product_id"`
	VirtualMachineID *string `json:"virtual_machine_id"`
	DiskSize         *string `json:"disk_size"`
	Type             *string `json:"type"`
	UpdatedAt        *string `json:"updated_at"`

	Status struct {
		Value      int64  `json:"value"`
		Label      string `json:"label"`
		Actionable bool   `json:"actionable"`
	} `json:"status"`
}

func (r *volumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

func (r *volumeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A CloudRaya block-storage volume. Supports the full lifecycle: create, read, " +
			"in-place updates (resize, attach/detach) and destroy.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Volume identifier.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Volume name. CloudRaya exposes no rename endpoint, so changing this " +
					"forces a new volume.",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"region_id": schema.StringAttribute{
				MarkdownDescription: "Region to create the volume in. Changing this forces a new volume.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"product_id": schema.StringAttribute{
				MarkdownDescription: "Storage product (capacity tier). Changing this resizes the volume in place.",
				Required:            true,
			},
			"virtual_machine_id": schema.StringAttribute{
				MarkdownDescription: "Virtual machine the volume is attached to. Setting, clearing or changing " +
					"this attaches and detaches in place; leave it unset for a detached volume.",
				Optional: true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Project that owns the volume. Defaults to the provider's `project_id`. " +
					"Changing this forces a new volume.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()},
			},
			"force_destroy": schema.BoolAttribute{
				MarkdownDescription: "Sent as `force` on destroy. An attached volume is always detached " +
					"before it is deleted, whatever this is set to. Defaults to `false`.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},

			// disk_size comes back as a decimal string ("10.000"); it is surfaced
			// as-is rather than coerced to a number the API never committed to.
			"disk_size_gb": schema.StringAttribute{MarkdownDescription: "Capacity in GB, from the product.", Computed: true},
			"volume_type":  schema.StringAttribute{MarkdownDescription: "ROOTDISK or DATADISK.", Computed: true},
			"status_label": schema.StringAttribute{MarkdownDescription: "Provisioning status label.", Computed: true},
			"updated_at":   schema.StringAttribute{MarkdownDescription: "Last update timestamp.", Computed: true},
		},
	}
}

func (r *volumeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *volumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan volumeModel
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
		"region_id":  plan.RegionID.ValueString(),
		"product_id": plan.ProductID.ValueString(),
		"project_id": projectID,
	}

	var created volumeAPI
	if err := r.client.Do(ctx, http.MethodPost, client.ServiceStorage, "/v1/volumes", body, &created); err != nil {
		resp.Diagnostics.AddError("Unable to create volume", err.Error())
		return
	}
	if created.ID == "" {
		resp.Diagnostics.AddError("Unable to create volume",
			"The API accepted the request but returned no volume id, so the resource cannot be tracked. "+
				"Check the CloudRaya console for an orphaned volume named "+plan.Name.ValueString()+".")
		return
	}

	// Create is asynchronous: the volume reports Provisioning with no type set
	// for a while. Returning early would store status_label="Provisioning" and
	// an empty volume_type, both of which drift once the volume settles.
	vol, err := r.waitForActive(ctx, created.ID, 15*time.Minute)
	if err != nil {
		resp.Diagnostics.AddError("Volume did not finish provisioning", err.Error()+
			"\n\nThe volume was created with id "+created.ID+" and is now tracked by Terraform.")
		if fallback, readErr := r.read(ctx, created.ID); readErr == nil {
			r.apply(fallback, &plan, projectID)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		}
		return
	}

	// Attach only after the volume is Active: the API rejects an attach while
	// volume_type is still unset during provisioning ("Only datadisk volume
	// type can be attached to virtual machine").
	if vmID := plan.VirtualMachineID.ValueString(); vmID != "" {
		if err := r.attach(ctx, created.ID, vmID); err != nil {
			resp.Diagnostics.AddError("Unable to attach volume", err.Error()+
				"\n\nThe volume was created with id "+created.ID+" and is now tracked by Terraform.")
			r.apply(vol, &plan, projectID)
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			return
		}
		if reread, err := r.read(ctx, created.ID); err == nil {
			vol = reread
		}
	}

	r.apply(vol, &plan, projectID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// waitForActive polls until the volume leaves its provisioning state.
func (r *volumeResource) waitForActive(ctx context.Context, id string, timeout time.Duration) (*volumeAPI, error) {
	deadline := time.Now().Add(timeout)
	const interval = 10 * time.Second

	for {
		vol, err := r.read(ctx, id)
		if err != nil {
			return nil, err
		}
		if !volumeTransitional(vol.Status.Label) {
			return vol, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s waiting for volume %s; last status was %q",
				timeout, id, vol.Status.Label)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (r *volumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vol, err := r.read(ctx, state.ID.ValueString())
	if err != nil {
		// Deleted outside Terraform: drop it from state so the next plan recreates it.
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read volume", err.Error())
		return
	}

	r.applyAll(vol, &state, state.ProjectID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update routes each changed attribute to its own endpoint: CloudRaya has no
// single "update volume" call, so a plan touching several fields becomes
// several requests.
func (r *volumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()

	if !plan.ProductID.Equal(state.ProductID) {
		if err := r.client.Do(ctx, http.MethodPatch, client.ServiceStorage,
			"/v1/volumes/"+id+"/change-package",
			map[string]any{"product_id": plan.ProductID.ValueString()}, nil); err != nil {
			resp.Diagnostics.AddError("Unable to resize volume", err.Error())
			return
		}
		// A resize reports "Processing" until it finishes, and an attach or
		// detach sent meanwhile is refused.
		if _, err := r.waitForActive(ctx, id, 15*time.Minute); err != nil {
			resp.Diagnostics.AddError("Volume did not finish resizing", err.Error())
			return
		}
	}

	if !plan.VirtualMachineID.Equal(state.VirtualMachineID) {
		oldVM := state.VirtualMachineID.ValueString()
		newVM := plan.VirtualMachineID.ValueString()

		// Moving between VMs needs a detach first; the volume can only be
		// attached in one place at a time.
		if oldVM != "" {
			if err := retryWhileBusy(ctx, 5*time.Minute, func() error { return r.detach(ctx, id) }); err != nil {
				resp.Diagnostics.AddError("Unable to detach volume", err.Error())
				return
			}
			if _, err := r.waitForActive(ctx, id, 10*time.Minute); err != nil {
				resp.Diagnostics.AddError("Volume did not settle after detach", err.Error())
				return
			}
		}
		if newVM != "" {
			if err := retryWhileBusy(ctx, 5*time.Minute, func() error { return r.attach(ctx, id, newVM) }); err != nil {
				resp.Diagnostics.AddError("Unable to attach volume", err.Error())
				return
			}
		}
	}

	// Wait for the volume to settle so computed attributes (capacity after a
	// resize) reflect the result rather than the pre-update values.
	vol, err := r.waitForActive(ctx, id, 10*time.Minute)
	if err != nil {
		resp.Diagnostics.AddError("Volume did not settle after update", err.Error())
		return
	}

	r.apply(vol, &plan, state.ProjectID.ValueString())
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *volumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The API refuses to delete an attached volume outright ("The volume has
	// not been detached from the Virtual Machine") regardless of `force` —
	// detaching first is mandatory, not just an option force_destroy skips.
	// Best-effort: state can say attached when the platform already detached
	// it (e.g. a previous destroy attempt got this far and failed later), and
	// that reads back as its own rejection ("has not been attached"). Either
	// way the delete call below is the real check; let it surface the truth.
	if vmID := state.VirtualMachineID.ValueString(); vmID != "" {
		_ = r.detach(ctx, state.ID.ValueString())
	}

	// The delete-time "still attached" check lags the detach confirmation —
	// seen with virtual_machine_id already reading null on the volume while
	// delete still refuses it — so retry through that window rather than
	// failing on what is actually a transient platform lag.
	err := retryWhileBusy(ctx, 2*time.Minute, func() error {
		return r.client.Do(ctx, http.MethodDelete, client.ServiceStorage,
			"/v1/volumes/"+state.ID.ValueString(),
			map[string]any{"force": state.ForceDestroy.ValueBool()}, nil)
	})
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Unable to destroy volume", err.Error())
		return
	}
}

func (r *volumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	// force_destroy has no API representation; seed the default so the first
	// plan after an import does not show a diff for it.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("force_destroy"), false)...)
}

func (r *volumeResource) read(ctx context.Context, id string) (*volumeAPI, error) {
	var vol volumeAPI
	if err := r.client.Do(ctx, http.MethodGet, client.ServiceStorage, "/v1/volumes/"+id, nil, &vol); err != nil {
		return nil, err
	}
	if vol.ID == "" {
		vol.ID = id
	}
	return &vol, nil
}

func (r *volumeResource) attach(ctx context.Context, id, vmID string) error {
	return r.client.Do(ctx, http.MethodPatch, client.ServiceStorage, "/v1/volumes/"+id+"/attach",
		map[string]any{"virtual_machine_id": vmID}, nil)
}

// detach never passes is_delete_volume: Terraform removes the volume through
// Delete, and letting a detach destroy it would strand the resource in state.
func (r *volumeResource) detach(ctx context.Context, id string) error {
	return r.client.Do(ctx, http.MethodPatch, client.ServiceStorage, "/v1/volumes/"+id+"/detach",
		map[string]any{"is_delete_volume": false}, nil)
}

// apply copies API values onto the model. Every field falls back to the value
// already in the model when the API omits it, so an unexpected payload
// degrades to "no drift detected" rather than to a spurious diff or a null in
// a computed attribute.
// volumeTransitional reports a status during which the volume is still being
// changed: "Provisioning" after create, "Processing" after a resize.
func volumeTransitional(label string) bool {
	return strings.EqualFold(label, "Provisioning") || strings.EqualFold(label, "Processing")
}

// applyAll refreshes configured attributes as well as computed ones; only Read
// uses it, where detecting drift is the point.
func (r *volumeResource) applyAll(vol *volumeAPI, m *volumeModel, projectID string) {
	if vol.Name != nil {
		m.Name = types.StringValue(*vol.Name)
	}
	if vol.RegionID != nil {
		m.RegionID = types.StringValue(*vol.RegionID)
	}
	if vol.ProductID != nil {
		m.ProductID = types.StringValue(*vol.ProductID)
	}
	if vol.VirtualMachineID != nil {
		if *vol.VirtualMachineID == "" {
			m.VirtualMachineID = types.StringNull()
		} else {
			m.VirtualMachineID = types.StringValue(*vol.VirtualMachineID)
		}
	} else {
		m.VirtualMachineID = types.StringNull()
	}
	r.apply(vol, m, projectID)
}

// apply fills only computed attributes. After create and update the
// configured ones must keep their planned values: the API reports a resize or
// a detach some time after accepting it, and echoing the stale value back is
// rejected by Terraform as an inconsistent result.
func (r *volumeResource) apply(vol *volumeAPI, m *volumeModel, projectID string) {
	m.ID = types.StringValue(vol.ID)

	switch {
	case vol.ProjectID != nil && *vol.ProjectID != "":
		m.ProjectID = types.StringValue(*vol.ProjectID)
	case projectID != "":
		m.ProjectID = types.StringValue(projectID)
	default:
		m.ProjectID = types.StringValue(r.client.ProjectID())
	}

	m.DiskSizeGB = stringOrEmpty(vol.DiskSize, m.DiskSizeGB)
	m.VolumeType = stringOrEmpty(vol.Type, m.VolumeType)
	m.StatusLabel = types.StringValue(vol.Status.Label)
	m.UpdatedAt = stringOrEmpty(vol.UpdatedAt, m.UpdatedAt)
}

// stringOrEmpty keeps computed attributes known: an unknown left in state after
// apply is a framework error, so a missing field becomes "" rather than null.
func stringOrEmpty(v *string, current types.String) types.String {
	if v != nil {
		return types.StringValue(*v)
	}
	if current.IsUnknown() || current.IsNull() {
		return types.StringValue("")
	}
	return current
}
