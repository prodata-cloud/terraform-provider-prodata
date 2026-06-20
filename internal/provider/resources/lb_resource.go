package resources

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"terraform-provider-prodata/internal/client"
	"terraform-provider-prodata/internal/tfutil"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                     = &LbResource{}
	_ resource.ResourceWithConfigure        = &LbResource{}
	_ resource.ResourceWithModifyPlan       = &LbResource{}
	_ resource.ResourceWithImportState      = &LbResource{}
	_ resource.ResourceWithConfigValidators = &LbResource{}
)

// LbResource implements the prodata_lb resource.
type LbResource struct {
	c *client.Client
}

// LbResourceModel mirrors the prodata_lb schema. backend_group is a pointer so the
// framework can distinguish "config block omitted" from "block present with all
// fields null" — both reach this struct as nil after Get().
type LbResourceModel struct {
	ID           types.Int64          `tfsdk:"id"`
	Region       types.String         `tfsdk:"region"`
	ProjectTag   types.String         `tfsdk:"project_tag"`
	Name         types.String         `tfsdk:"name"`
	Description  types.String         `tfsdk:"description"`
	Type         types.String         `tfsdk:"type"`
	Protocol     types.String         `tfsdk:"protocol"`
	NetworkID    types.Int64          `tfsdk:"network_id"`
	Port         []LbPortModel        `tfsdk:"port"`
	BackendGroup *LbBackendGroupModel `tfsdk:"backend_group"`
	Source       types.String         `tfsdk:"source"`
	Status       types.String         `tfsdk:"status"`
	PublicIP     types.String         `tfsdk:"public_ip"`
	PrivateIP    types.String         `tfsdk:"private_ip"`
	DateCreated  types.String         `tfsdk:"date_created"`
	Timeouts     timeouts.Value       `tfsdk:"timeouts"`
}

type LbPortModel struct {
	Port       types.Int64 `tfsdk:"port"`
	TargetPort types.Int64 `tfsdk:"target_port"`
}

type LbBackendGroupModel struct {
	VMIDs      types.Set   `tfsdk:"vm_ids"`
	NodePoolID types.Int64 `tfsdk:"node_pool_id"`
}

// Default polling and timeout values. Polling is fixed 30s; create/update/delete
// timeouts can be overridden via the `timeouts` block.
const (
	lbPollInterval       = 30 * time.Second
	lbDefaultCreateTime  = 30 * time.Minute
	lbDefaultUpdateTime  = 30 * time.Minute
	lbDefaultDeleteTime  = 15 * time.Minute
	lbMaxConsecutiveErrs = 1 // one immediate retry on transient 5xx during polling
)

func NewLbResource() resource.Resource {
	return &LbResource{}
}

func (r *LbResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_lb"
}

func (r *LbResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a ProData load balancer (L4 TCP/UDP). " +
			"Backed by two hidden nginx VMs provisioned in the target network. " +
			"Backends receive traffic in round-robin order. " +
			"Backend group may be either a set of VM guids (`vm_ids`) or a Kubernetes " +
			"node pool id (`node_pool_id`) — switching between modes forces a new resource.",
		Attributes: map[string]schema.Attribute{
			"id": schema.Int64Attribute{
				MarkdownDescription: "Load balancer ID, assigned by the panel.",
				Computed:            true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID. If omitted, uses the provider default.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag the load balancer belongs to. If omitted, uses the provider default.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Load balancer name. 3-63 characters, letters / digits / hyphens, must not " +
					"start or end with a hyphen. Unique within the parent organization and region. Updated in place.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(lbMinNameLen, lbMaxNameLen),
					stringvalidator.RegexMatches(lbNameRegex,
						"must contain only letters, digits and hyphens, and must not start or end with a hyphen"),
				},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: "Free-form description. Updated in place.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "Load balancer type: `external` (public IP) or `internal` (private IP only). " +
					"Changing this forces a new resource.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf(client.LbTypeExternal, client.LbTypeInternal),
				},
			},
			"protocol": schema.StringAttribute{
				MarkdownDescription: "L4 protocol: `TCP` or `UDP`. Case-sensitive — any other value is rejected " +
					"at plan time (the server silently downgrades unknown values to TCP). Applies to every " +
					"`port` block; mixed protocols on one balancer are not supported. " +
					"Changing this forces a new resource.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf(client.LbProtocolTCP, client.LbProtocolUDP),
				},
			},
			"network_id": schema.Int64Attribute{
				MarkdownDescription: "Local network ID. For VM backends, every VM must belong to this network. " +
					"For Kubernetes node pool backends, this must match the cluster's network. " +
					"Changing this forces a new resource.",
				Required: true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
				Validators: []validator.Int64{
					int64validator.AtLeast(1),
				},
			},
			"source": schema.StringAttribute{
				MarkdownDescription: "Backend source as reported by the panel: `FRONTEND` (VM backends) " +
					"or `CCM` (Kubernetes node pool backend). Computed.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Current lifecycle status: `NEW`, `PROCESSING`, `SUCCESS`, `DELETED`, or `FAIL`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"public_ip": schema.StringAttribute{
				MarkdownDescription: "Public IP assigned to external load balancers. Null for internal " +
					"balancers and transiently null while status is `NEW`.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"private_ip": schema.StringAttribute{
				MarkdownDescription: "Private VIP inside `network_id`. May be transiently null while status is `NEW`.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"date_created": schema.StringAttribute{
				MarkdownDescription: "Server-reported creation timestamp.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"port": schema.SetNestedAttribute{
				MarkdownDescription: "Port mappings. 1 to 10 blocks. Re-ordering blocks does not produce a diff " +
					"(set semantics).",
				Required: true,
				Validators: []validator.Set{
					setvalidator.SizeBetween(lbMinPorts, lbMaxPorts),
				},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"port": schema.Int64Attribute{
							MarkdownDescription: "Port on the load balancer (1-65535).",
							Required:            true,
							Validators: []validator.Int64{
								int64validator.Between(1, 65535),
							},
						},
						"target_port": schema.Int64Attribute{
							MarkdownDescription: "Port on each backend (1-65535).",
							Required:            true,
							Validators: []validator.Int64{
								int64validator.Between(1, 65535),
							},
						},
					},
				},
			},
			"backend_group": schema.SingleNestedAttribute{
				MarkdownDescription: "Exactly one of `vm_ids` or `node_pool_id` must be set. Switching between " +
					"modes forces a new resource; same-mode content changes are applied in place.",
				Required: true,
				Attributes: map[string]schema.Attribute{
					"vm_ids": schema.SetAttribute{
						MarkdownDescription: "Set of VM **guids** — the computed `guid` attribute on `prodata_vm` (NOT the numeric id). " +
							"At least one entry required when this mode is used. Re-ordering produces no diff.",
						Optional:    true,
						ElementType: types.StringType,
						Validators: []validator.Set{
							setvalidator.SizeAtLeast(1),
						},
					},
					"node_pool_id": schema.Int64Attribute{
						MarkdownDescription: "Kubernetes node pool ID. Whole-pool only (no partial backends). " +
							"Changing the pool requires destroy+recreate in v0.",
						Optional: true,
						PlanModifiers: []planmodifier.Int64{
							int64planmodifier.RequiresReplace(),
						},
					},
				},
			},
			"timeouts": timeouts.Attributes(ctx, timeouts.Opts{
				Create: true,
				Update: true,
				Delete: true,
			}),
		},
	}
}

func (r *LbResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	r.c = c
}

// ConfigValidators enforces ExactlyOneOf vm_ids / node_pool_id inside backend_group.
// SingleNestedAttribute fields can't host that validator on themselves directly, so
// it lives at the resource level with explicit path expressions.
func (r *LbResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(
			path.MatchRoot("backend_group").AtName("vm_ids"),
			path.MatchRoot("backend_group").AtName("node_pool_id"),
		),
	}
}

// ModifyPlan handles two concerns:
//   - CCM description: the panel owns the description of CCM (node pool) load
//     balancers — it hard-codes "CCM: <name>" at create time and ignores
//     caller-supplied values. Reject a user-set description for CCM balancers on
//     both create and update so the constraint surfaces at plan time rather than
//     silently. (Description is Computed, so omitting it in HCL is valid; the
//     panel value reads back into state.)
//   - Update: mode-switches (vm_ids <-> node_pool_id) mark backend_group as
//     requires-replace. Same-mode content changes pass through to Update.
func (r *LbResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan LbResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var config LbResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	_, hasPool := backendMode(plan.BackendGroup)
	descriptionSet := !config.Description.IsNull() && !config.Description.IsUnknown()
	if summary := validateCCMDescriptionNotConfigurable(hasPool, descriptionSet); summary != "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("description"),
			summary,
			"The panel controls the description of CCM (node pool) load balancers — it "+
				"sets \"CCM: <name>\" and ignores caller-supplied values. Remove the "+
				"`description` attribute from your configuration and let the provider read "+
				"the panel's value back into state.",
		)
		return
	}

	if req.State.Raw.IsNull() {
		return
	}
	var state LbResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	stateHasVMs, stateHasPool := backendMode(state.BackendGroup)
	planHasVMs, planHasPool := backendMode(plan.BackendGroup)
	if detectModeSwitch(stateHasVMs, stateHasPool, planHasVMs, planHasPool) {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("backend_group"))
	}
}

// ---- Create ----

func (r *LbResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan LbResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	region, projectTag := r.resolveScope(plan.Region, plan.ProjectTag)
	opts := &client.RequestOpts{Region: region, ProjectTag: projectTag}

	createTimeout, diags := plan.Timeouts.Create(ctx, lbDefaultCreateTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()

	var (
		lb  *client.LoadBalancer
		err error
	)

	hasVMs, hasPool := backendMode(plan.BackendGroup)
	switch {
	case hasVMs:
		ports, perr := portsToWire(plan.Port)
		if perr != nil {
			resp.Diagnostics.AddError("Invalid port configuration", perr.Error())
			return
		}
		backends, berr := vmIDsToWire(ctx, plan.BackendGroup.VMIDs)
		if berr != nil {
			resp.Diagnostics.AddError("Invalid backend_group.vm_ids", berr.Error())
			return
		}
		wire := client.LoadBalancerRequest{
			Name:        plan.Name.ValueString(),
			Description: plan.Description.ValueString(),
			IsPublic:    plan.Type.ValueString() == client.LbTypeExternal,
			Protocol:    plan.Protocol.ValueString(),
			UserNetID:   plan.NetworkID.ValueInt64(),
			Backends:    backends,
			Ports:       ports,
		}
		tflog.Debug(ctx, "Creating frontend load balancer", map[string]any{
			"name":        wire.Name,
			"region":      region,
			"project_tag": projectTag,
			"backends":    len(wire.Backends),
			"ports":       len(wire.Ports),
		})
		lb, err = client.RetryOnBusy(ctx, client.RetryTimeoutLong, func() (*client.LoadBalancer, error) {
			return r.c.CreateLoadBalancerFrontend(ctx, wire, opts)
		})

	case hasPool:
		ports, perr := portsToWire(plan.Port)
		if perr != nil {
			resp.Diagnostics.AddError("Invalid port configuration", perr.Error())
			return
		}
		wire := client.CreateLoadBalancerCCMRequest{
			Name:       plan.Name.ValueString(),
			NodePoolID: plan.BackendGroup.NodePoolID.ValueInt64(),
			IsPublic:   plan.Type.ValueString() == client.LbTypeExternal,
			Protocol:   plan.Protocol.ValueString(),
			Ports:      ports,
		}
		tflog.Debug(ctx, "Creating CCM load balancer", map[string]any{
			"name":         wire.Name,
			"region":       region,
			"project_tag":  projectTag,
			"node_pool_id": wire.NodePoolID,
			"ports":        len(wire.Ports),
		})
		lb, err = client.RetryOnBusy(ctx, client.RetryTimeoutLong, func() (*client.LoadBalancer, error) {
			return r.c.CreateLoadBalancerCCM(ctx, wire, opts)
		})

	default:
		resp.Diagnostics.AddError("Internal error", "backend_group has neither vm_ids nor node_pool_id (validator should have caught this)")
		return
	}

	if err != nil {
		if client.IsInsufficientFreeIPs(err) {
			resp.Diagnostics.AddError(
				"Insufficient free IPs in the local network",
				fmt.Sprintf("The network needs at least three free IPs (one VIP plus two for the hidden nginx VMs). %s", err.Error()),
			)
			return
		}
		resp.Diagnostics.AddError("Unable to create load balancer", client.LBErrorDetail(err))
		return
	}

	final, waitErr := r.waitForTerminalStatus(ctx, lb.ID, opts, lbTerminalApply)
	resultLB := final
	if resultLB == nil {
		resultLB = lb
	}

	r.applyServerState(&plan, resultLB, region, projectTag)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Load balancer did not reach a successful state",
			fmt.Sprintf("LB %d: %s", resultLB.ID, waitErr.Error()),
		)
		return
	}
}

// ---- Read ----

func (r *LbResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data LbResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.optsFromState(data.Region, data.ProjectTag)
	id := data.ID.ValueInt64()

	lb, err := r.c.GetLoadBalancer(ctx, id, opts)
	if err != nil {
		if client.IsNotFound(err) {
			tflog.Warn(ctx, "Load balancer not found, removing from state", map[string]any{"id": id})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read load balancer", client.LBErrorDetail(err))
		return
	}
	if lb.Status == client.LbStatusDeleted {
		tflog.Warn(ctx, "Load balancer reported DELETED, removing from state", map[string]any{"id": id})
		resp.State.RemoveResource(ctx)
		return
	}

	region := data.Region.ValueString()
	if region == "" {
		region = r.c.Region
	}
	projectTag := data.ProjectTag.ValueString()
	if projectTag == "" {
		projectTag = r.c.ProjectTag
	}
	r.applyServerState(&data, lb, region, projectTag)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// ---- Update ----

func (r *LbResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan LbResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.optsFromState(state.Region, state.ProjectTag)
	id := state.ID.ValueInt64()
	source := state.Source.ValueString()

	updateTimeout, diags := plan.Timeouts.Update(ctx, lbDefaultUpdateTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()

	ports, perr := portsToWire(plan.Port)
	if perr != nil {
		resp.Diagnostics.AddError("Invalid port configuration", perr.Error())
		return
	}

	wire := client.LoadBalancerRequest{
		Name:        plan.Name.ValueString(),
		Description: plan.Description.ValueString(),
		IsPublic:    plan.Type.ValueString() == client.LbTypeExternal,
		Protocol:    plan.Protocol.ValueString(),
		UserNetID:   plan.NetworkID.ValueInt64(),
		Ports:       ports,
	}

	switch source {
	// Empty source = a legacy LB imported before source dispatch existed; route it
	// through the frontend configure path (mirroring Delete), so an imported pre-source
	// LB stays updatable instead of hard-failing into destroy/recreate.
	case client.LbSourceFrontend, "":
		backends, berr := vmIDsToWire(ctx, plan.BackendGroup.VMIDs)
		if berr != nil {
			resp.Diagnostics.AddError("Invalid backend_group.vm_ids", berr.Error())
			return
		}
		wire.Backends = backends
		tflog.Debug(ctx, "Configuring frontend load balancer", map[string]any{
			"id":       id,
			"name":     wire.Name,
			"backends": len(wire.Backends),
			"ports":    len(wire.Ports),
		})
		if _, err := client.RetryOnBusy(ctx, client.RetryTimeoutLong, func() (*client.LoadBalancer, error) {
			return r.c.ConfigureLoadBalancerFrontend(ctx, id, wire, opts)
		}); err != nil {
			resp.Diagnostics.AddError("Unable to update load balancer", client.LBErrorDetail(err))
			return
		}
	case client.LbSourceCCM:
		// CCM backends are derived from the node pool; the configure endpoint ignores
		// the absent backends field (and node_pool_id swaps are gated by RequiresReplace).
		wire.Backends = nil
		tflog.Debug(ctx, "Configuring CCM load balancer", map[string]any{
			"id":    id,
			"name":  wire.Name,
			"ports": len(wire.Ports),
		})
		if _, err := client.RetryOnBusy(ctx, client.RetryTimeoutLong, func() (*client.LoadBalancer, error) {
			return r.c.ConfigureLoadBalancerCCM(ctx, id, wire, opts)
		}); err != nil {
			resp.Diagnostics.AddError("Unable to update load balancer", client.LBErrorDetail(err))
			return
		}
	default:
		resp.Diagnostics.AddError(
			"Unknown load balancer source",
			fmt.Sprintf("State has source=%q; expected %q or %q. The resource may have been imported before this provider knew about source dispatch — destroy and recreate.",
				source, client.LbSourceFrontend, client.LbSourceCCM),
		)
		return
	}

	final, waitErr := r.waitForTerminalStatus(ctx, id, opts, lbTerminalApply)
	resultLB := final
	if resultLB == nil {
		// fall back to a single read so state still reflects the server
		resultLB, _ = r.c.GetLoadBalancer(ctx, id, opts)
	}
	if resultLB != nil {
		region := state.Region.ValueString()
		if region == "" {
			region = r.c.Region
		}
		projectTag := state.ProjectTag.ValueString()
		if projectTag == "" {
			projectTag = r.c.ProjectTag
		}
		r.applyServerState(&plan, resultLB, region, projectTag)
		// Preserve prior date_created — panel-main's configure endpoint resets it
		// to now() (LoadBalancerCoreService.java:840), which would otherwise fail
		// Terraform's "computed output must be consistent" check during apply.
		plan.DateCreated = state.DateCreated
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if waitErr != nil {
		resp.Diagnostics.AddError(
			"Load balancer did not reach a successful state after update",
			fmt.Sprintf("LB %d: %s", id, waitErr.Error()),
		)
		return
	}
}

// ---- Delete ----

func (r *LbResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data LbResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := r.optsFromState(data.Region, data.ProjectTag)
	id := data.ID.ValueInt64()
	source := data.Source.ValueString()

	deleteTimeout, diags := data.Timeouts.Delete(ctx, lbDefaultDeleteTime)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, deleteTimeout)
	defer cancel()

	var err error
	switch source {
	case client.LbSourceCCM:
		err = r.c.DeleteLoadBalancerCCM(ctx, id, opts)
	case client.LbSourceFrontend, "":
		// "" handles legacy imports where Source was never populated; default to frontend.
		err = r.c.DeleteLoadBalancerFrontend(ctx, id, opts)
	default:
		resp.Diagnostics.AddError(
			"Unknown load balancer source",
			fmt.Sprintf("State has source=%q; expected %q or %q.", source, client.LbSourceFrontend, client.LbSourceCCM),
		)
		return
	}
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to delete load balancer", client.LBErrorDetail(err))
		return
	}

	if _, waitErr := r.waitForTerminalStatus(ctx, id, opts, lbTerminalDelete); waitErr != nil {
		// terminal-after-delete is "removed" (IsNotFound) or status == DELETED; anything
		// else means the panel is still working on it past the timeout.
		resp.Diagnostics.AddError(
			"Load balancer did not finish deleting",
			fmt.Sprintf("LB %d: %s", id, waitErr.Error()),
		)
		return
	}
}

// ---- ImportState ----

func (r *LbResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, region, projectTag, err := parseLBImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected a load balancer ID or `{region}/{id}@{project_tag}`, got: %q\n\n"+
				"Examples:\n  terraform import prodata_lb.example 42\n"+
				"  terraform import prodata_lb.example UZ-5/42@my-project", req.ID),
		)
		return
	}
	// A bare-id import (no scope in the string) falls back to the provider defaults.
	if region == "" {
		region = r.c.Region
	}
	if projectTag == "" {
		projectTag = r.c.ProjectTag
	}
	tflog.Info(ctx, "Importing load balancer", map[string]any{"id": id, "region": region, "project_tag": projectTag})
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), region)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_tag"), projectTag)...)
}

// parseLBImportID accepts either a bare integer id (scoped to the provider
// defaults) or the composite "{region}/{id}@{project_tag}" form. Region and
// project_tag are returned empty for the bare form so the caller can apply
// provider defaults.
func parseLBImportID(s string) (id int64, region, projectTag string, err error) {
	if strings.ContainsAny(s, "/@") {
		slash := strings.IndexByte(s, '/')
		at := strings.LastIndexByte(s, '@')
		if slash <= 0 || at <= slash+1 || at >= len(s)-1 {
			return 0, "", "", fmt.Errorf("malformed composite import id %q", s)
		}
		region = s[:slash]
		idStr := s[slash+1 : at]
		projectTag = s[at+1:]
		id, perr := strconv.ParseInt(idStr, 10, 64)
		if perr != nil {
			return 0, "", "", fmt.Errorf("id segment %q is not an integer", idStr)
		}
		return id, region, projectTag, nil
	}
	id, perr := strconv.ParseInt(s, 10, 64)
	if perr != nil {
		return 0, "", "", fmt.Errorf("%q is not an integer", s)
	}
	return id, "", "", nil
}

// ---- helpers ----

// resolveScope falls back to the provider-level Region/ProjectTag when the resource
// attributes are unset.
func (r *LbResource) resolveScope(region, projectTag types.String) (string, string) {
	rg := region.ValueString()
	if rg == "" {
		rg = r.c.Region
	}
	pt := projectTag.ValueString()
	if pt == "" {
		pt = r.c.ProjectTag
	}
	return rg, pt
}

func (r *LbResource) optsFromState(region, projectTag types.String) *client.RequestOpts {
	opts := &client.RequestOpts{}
	if !region.IsNull() && !region.IsUnknown() && region.ValueString() != "" {
		opts.Region = region.ValueString()
	}
	if !projectTag.IsNull() && !projectTag.IsUnknown() && projectTag.ValueString() != "" {
		opts.ProjectTag = projectTag.ValueString()
	}
	return opts
}

// applyServerState writes computed fields and re-syncs backend membership from a
// server-returned LoadBalancer onto the model. node_pool_id is preserved from the
// caller's model (plan on Create/Update, prior state on Read) because the panel
// does not surface it in the GET response.
func (r *LbResource) applyServerState(m *LbResourceModel, lb *client.LoadBalancer, region, projectTag string) {
	m.ID = types.Int64Value(lb.ID)
	m.Region = types.StringValue(region)
	m.ProjectTag = types.StringValue(projectTag)
	m.Name = types.StringValue(lb.Name)
	m.Description = tfutil.StringOrNull(lb.Description)
	m.Type = types.StringValue(lb.Type)
	m.Protocol = types.StringValue(lb.Protocol)
	m.NetworkID = types.Int64Value(lb.NetworkID)
	m.Source = types.StringValue(lb.Source)
	m.Status = types.StringValue(lb.Status)
	m.PublicIP = tfutil.StringOrNull(lb.PublicIP)
	m.PrivateIP = tfutil.StringOrNull(lb.PrivateIP)
	m.DateCreated = tfutil.StringOrNull(lb.DateCreated)

	m.Port = portsFromServer(lb.Ports)

	// Backend group: FRONTEND populates vm_ids; CCM preserves the existing
	// node_pool_id from state (the server does not surface it).
	switch lb.Source {
	case client.LbSourceFrontend:
		m.BackendGroup = &LbBackendGroupModel{
			VMIDs:      vmIDsFromServer(lb.Backends),
			NodePoolID: types.Int64Null(),
		}
	case client.LbSourceCCM:
		nodePool := types.Int64Null()
		if m.BackendGroup != nil {
			nodePool = m.BackendGroup.NodePoolID
		}
		m.BackendGroup = &LbBackendGroupModel{
			VMIDs:      types.SetNull(types.StringType),
			NodePoolID: nodePool,
		}
	default:
		// Unknown source — leave backend_group untouched.
	}
}

// waitForTerminalStatus polls GetLoadBalancer at lbPollInterval until the LB
// reaches one of the terminal statuses, the context is cancelled, or the
// resource is removed. terminalRemoved=true means IsNotFound is itself a
// terminal success (Delete path). Returns the final LB (may be nil if removed)
// and an error if the status is a terminal failure or the wait exhausts.
func (r *LbResource) waitForTerminalStatus(ctx context.Context, id int64, opts *client.RequestOpts, t lbTerminalSet) (*client.LoadBalancer, error) {
	var consecutiveErrs int
	var last *client.LoadBalancer

	for {
		lb, err := r.c.GetLoadBalancer(ctx, id, opts)
		switch {
		case err == nil:
			consecutiveErrs = 0
			last = lb
			tflog.Debug(ctx, "Polling load balancer", map[string]any{
				"id":     id,
				"status": lb.Status,
			})
			switch {
			case t.isSuccess(lb.Status):
				return lb, nil
			case t.isFailure(lb.Status):
				return lb, fmt.Errorf("terminal status %s", lb.Status)
			}
		case client.IsNotFound(err):
			if t.removedIsSuccess {
				return nil, nil
			}
			return nil, err
		default:
			consecutiveErrs++
			tflog.Warn(ctx, "Transient error polling load balancer", map[string]any{
				"id":                 id,
				"error":              err.Error(),
				"consecutive_errors": consecutiveErrs,
			})
			if consecutiveErrs > lbMaxConsecutiveErrs {
				return last, fmt.Errorf("polling failed after %d consecutive errors: %w", consecutiveErrs, err)
			}
		}

		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-time.After(lbPollInterval):
		}
	}
}

// lbTerminalSet describes which statuses end a polling loop and how to interpret them.
type lbTerminalSet struct {
	successStatuses  map[string]struct{}
	failureStatuses  map[string]struct{}
	removedIsSuccess bool
}

func (t lbTerminalSet) isSuccess(s string) bool {
	_, ok := t.successStatuses[s]
	return ok
}

func (t lbTerminalSet) isFailure(s string) bool {
	_, ok := t.failureStatuses[s]
	return ok
}

// lbTerminalApply is the Create/Update wait set. SUCCESS = good; FAIL or DELETED
// are terminal errors (DELETED during create/update is anomalous — scheduler aborted).
var lbTerminalApply = lbTerminalSet{
	successStatuses: map[string]struct{}{
		client.LbStatusSuccess: {},
	},
	failureStatuses: map[string]struct{}{
		client.LbStatusFail:    {},
		client.LbStatusDeleted: {},
	},
}

// lbTerminalDelete is the Delete wait set. Either IsNotFound (already removed) or
// status == DELETED is a success; FAIL is a terminal error.
var lbTerminalDelete = lbTerminalSet{
	successStatuses: map[string]struct{}{
		client.LbStatusDeleted: {},
	},
	failureStatuses: map[string]struct{}{
		client.LbStatusFail: {},
	},
	removedIsSuccess: true,
}

// portsToWire flattens the resource-model port list into the wire shape.
func portsToWire(in []LbPortModel) ([]client.LbPortReq, error) {
	out := make([]client.LbPortReq, 0, len(in))
	for i, p := range in {
		if p.Port.IsNull() || p.TargetPort.IsNull() {
			return nil, fmt.Errorf("port[%d]: port and target_port are required", i)
		}
		out = append(out, client.LbPortReq{
			BalancerPort: int32(p.Port.ValueInt64()),
			BackendPort:  int32(p.TargetPort.ValueInt64()),
		})
	}
	return out, nil
}

// portsFromServer maps client.LbPort back to the model.
func portsFromServer(in []client.LbPort) []LbPortModel {
	if len(in) == 0 {
		return nil
	}
	out := make([]LbPortModel, 0, len(in))
	for _, p := range in {
		out = append(out, LbPortModel{
			Port:       types.Int64Value(int64(p.Port)),
			TargetPort: types.Int64Value(int64(p.TargetPort)),
		})
	}
	return out
}

// vmIDsToWire extracts the VM guid strings from the vm_ids Set into the wire shape.
func vmIDsToWire(ctx context.Context, ids types.Set) ([]client.LbBackendRef, error) {
	if ids.IsNull() || ids.IsUnknown() {
		return nil, errors.New("vm_ids is required for frontend backends")
	}
	var raw []string
	if diags := ids.ElementsAs(ctx, &raw, false); diags.HasError() {
		return nil, fmt.Errorf("decode vm_ids: %s", diagsString(diags))
	}
	out := make([]client.LbBackendRef, 0, len(raw))
	for _, g := range raw {
		out = append(out, client.LbBackendRef{UserVmID: g})
	}
	return out, nil
}

// vmIDsFromServer builds a Set[String] of guids from the server response.
func vmIDsFromServer(in []client.LbBackend) types.Set {
	if len(in) == 0 {
		return types.SetValueMust(types.StringType, []attr.Value{})
	}
	values := make([]attr.Value, 0, len(in))
	for _, b := range in {
		values = append(values, types.StringValue(b.Guid))
	}
	return types.SetValueMust(types.StringType, values)
}

// ---- pure helpers (unit-testable) ----

const (
	lbMinPorts   = 1
	lbMaxPorts   = 10
	lbMinNameLen = 3
	lbMaxNameLen = 63
)

// lbNameRegex enforces letters/digits/hyphens with no leading or trailing
// hyphen. The panel itself does not validate the name, but the hidden nginx
// VMs derive their hostnames from it, so we want a Linux-hostname-shaped value.
var lbNameRegex = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?$`)

// backendMode reports which mode the given backend_group block expresses. A pure
// helper so ModifyPlan and Create/Update share the same definition.
func backendMode(bg *LbBackendGroupModel) (hasVMs, hasPool bool) {
	if bg == nil {
		return false, false
	}
	hasVMs = !bg.VMIDs.IsNull() && !bg.VMIDs.IsUnknown()
	hasPool = !bg.NodePoolID.IsNull() && !bg.NodePoolID.IsUnknown()
	return
}

// detectModeSwitch returns true iff one mode is set in state and the other in plan
// (i.e. the user moved from vm_ids to node_pool_id or vice versa).
func detectModeSwitch(stateVMs, statePool, planVMs, planPool bool) bool {
	if stateVMs && planPool {
		return true
	}
	if statePool && planVMs {
		return true
	}
	return false
}

// validateCCMDescriptionNotConfigurable mirrors the ModifyPlan rule that rejects
// a user-supplied description on CCM (node_pool_id) load balancers. The panel
// owns the description ("CCM: <name>") and ignores caller-supplied values on both
// create and configure, so we fail the plan to surface the constraint rather
// than let the value silently diverge. Returns "" if OK, else the error summary.
func validateCCMDescriptionNotConfigurable(hasPool, descriptionSet bool) string {
	if hasPool && descriptionSet {
		return "description not configurable for CCM load balancers"
	}
	return ""
}

// diagsString turns a framework diagnostics value into a single error message.
func diagsString(d diag.Diagnostics) string {
	errs := d.Errors()
	if len(errs) == 0 {
		return "unknown error"
	}
	return errs[0].Summary() + ": " + errs[0].Detail()
}
