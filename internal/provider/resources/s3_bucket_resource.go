package resources

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &S3BucketResource{}
	_ resource.ResourceWithConfigure   = &S3BucketResource{}
	_ resource.ResourceWithModifyPlan  = &S3BucketResource{}
	_ resource.ResourceWithImportState = &S3BucketResource{}
)

type S3BucketResource struct {
	c *client.Client
}

type S3BucketResourceModel struct {
	ID                types.String `tfsdk:"id"`
	Region            types.String `tfsdk:"region"`
	ProjectTag        types.String `tfsdk:"project_tag"`
	Name              types.String `tfsdk:"name"`
	Acl               types.String `tfsdk:"acl"`
	Versioning        types.String `tfsdk:"versioning"`
	ObjectLockEnabled types.Bool   `tfsdk:"object_lock_enabled"`
	ForceDestroy      types.Bool   `tfsdk:"force_destroy"`
	CreationDate      types.String `tfsdk:"creation_date"`
}

func NewS3BucketResource() resource.Resource {
	return &S3BucketResource{}
}

func (r *S3BucketResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_s3_bucket"
}

func (r *S3BucketResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a ProData S3 (Ceph RGW) bucket. " +
			"Buckets are scoped to a single project; cross-project name conflicts surface as a clear error. " +
			"ACL is trust-state (no drift detection — cannot round-trip canned ACL through S3 grants); " +
			"`versioning` and `object_lock_enabled` are drift-detected.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Resource identifier (equal to `name`).",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID. If omitted, uses provider default.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag the bucket belongs to. If omitted, uses provider default.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Bucket name. 3-24 chars, lowercase letters, digits, dots and hyphens. " +
					"No leading/trailing or consecutive separators. Changing this forces a new resource.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					bucketNameValidator{},
				},
			},
			"acl": schema.StringAttribute{
				MarkdownDescription: "Canned ACL: `private`, `public-read`, or `public-read-write`. " +
					"Updated in place. Not drift-detected.",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("private"),
				Validators: []validator.String{
					stringvalidator.OneOf("private", "public-read", "public-read-write"),
				},
			},
			"versioning": schema.StringAttribute{
				MarkdownDescription: "Versioning state: `enabled`, `suspended`, or `disabled`. " +
					"`disabled` is the never-touched state — once enabled or suspended, cannot transition back.",
				Optional: true,
				Computed: true,
				Default:  stringdefault.StaticString("disabled"),
				Validators: []validator.String{
					stringvalidator.OneOf("enabled", "suspended", "disabled"),
				},
			},
			"object_lock_enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether S3 object lock is enabled. Requires `versioning = \"enabled\"`. " +
					"Cannot be changed after creation.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{
					boolplanmodifier.RequiresReplace(),
				},
			},
			"force_destroy": schema.BoolAttribute{
				MarkdownDescription: "If `true`, `terraform destroy` wipes all objects, versions, and " +
					"multipart uploads inside the bucket before deleting. If `false` (default), destroy " +
					"refuses on a non-empty bucket and the bucket survives.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"creation_date": schema.StringAttribute{
				MarkdownDescription: "Server-reported bucket creation timestamp (ISO-8601).",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *S3BucketResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this to the provider developers.", req.ProviderData),
		)
		return
	}
	r.c = c
}

func (r *S3BucketResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan S3BucketResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !plan.ObjectLockEnabled.IsUnknown() && !plan.Versioning.IsUnknown() {
		if msg := validateObjectLockRequiresVersioning(plan.ObjectLockEnabled.ValueBool(), plan.Versioning.ValueString()); msg != "" {
			resp.Diagnostics.AddAttributeError(path.Root("object_lock_enabled"), "Invalid configuration", msg)
		}
	}

	if !req.State.Raw.IsNull() {
		var state S3BucketResourceModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if !state.Versioning.IsNull() && !state.Versioning.IsUnknown() && !plan.Versioning.IsUnknown() {
			if msg := validateVersioningTransition(state.Versioning.ValueString(), plan.Versioning.ValueString()); msg != "" {
				resp.Diagnostics.AddAttributeError(path.Root("versioning"), "Invalid versioning transition", msg)
			}
		}
	}
}

func (r *S3BucketResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan S3BucketResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	region := plan.Region.ValueString()
	if region == "" {
		region = r.c.Region
	}
	projectTag := plan.ProjectTag.ValueString()
	if projectTag == "" {
		projectTag = r.c.ProjectTag
	}
	opts := &client.RequestOpts{Region: region, ProjectTag: projectTag}

	name := plan.Name.ValueString()
	createReq := client.CreateBucketRequest{
		BucketKey: name,
		Acl:       aclToEnum(plan.Acl.ValueString()),
	}
	if vc := versioningToConfig(plan.Versioning.ValueString()); vc != nil {
		createReq.VersioningConfiguration = vc
	}
	if plan.ObjectLockEnabled.ValueBool() {
		t := true
		createReq.ObjectLockEnabledForBucket = &t
	}

	tflog.Debug(ctx, "Creating bucket", map[string]any{
		"name":                name,
		"region":              region,
		"project_tag":         projectTag,
		"acl":                 createReq.Acl,
		"versioning":          plan.Versioning.ValueString(),
		"object_lock_enabled": plan.ObjectLockEnabled.ValueBool(),
	})

	err := r.c.CreateBucket(ctx, createReq, opts)
	if err != nil && client.IsAPIError(err, 626) {
		// 626 = name conflict. Verify ownership before adopting: same-project → adopt,
		// other-project → loud error (never silent-drop someone else's bucket).
		tflog.Info(ctx, "Bucket name conflict (626), checking ownership for adoption", map[string]any{"name": name})
		existing, getErr := r.c.GetBucket(ctx, name, opts)
		if getErr != nil {
			if client.IsAPIError(getErr, 712) {
				resp.Diagnostics.AddError(
					"Bucket name taken by another project",
					fmt.Sprintf("A bucket named %q already exists but belongs to a different project. Choose a different name.", name),
				)
				return
			}
			resp.Diagnostics.AddError(
				"Unable to verify bucket conflict",
				fmt.Sprintf("CreateBucket returned 626 but verifying ownership failed: %s", getErr.Error()),
			)
			return
		}
		tflog.Info(ctx, "Adopting pre-existing bucket in same project", map[string]any{"name": name})
		if rfErr := r.refreshFromServer(ctx, &plan, existing, opts); rfErr != nil {
			resp.Diagnostics.AddError("Unable to read adopted Bucket configuration", rfErr.Error())
			return
		}
		plan.ID = types.StringValue(existing.Name)
		plan.Name = types.StringValue(existing.Name)
		plan.Region = types.StringValue(region)
		plan.ProjectTag = types.StringValue(projectTag)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create Bucket", err.Error())
		return
	}

	fresh, getErr := r.c.GetBucket(ctx, name, opts)
	if getErr != nil {
		resp.Diagnostics.AddError("Unable to Read Bucket after Create", getErr.Error())
		return
	}
	if rfErr := r.refreshFromServer(ctx, &plan, fresh, opts); rfErr != nil {
		resp.Diagnostics.AddError("Unable to Read Bucket configuration", rfErr.Error())
		return
	}
	plan.ID = types.StringValue(fresh.Name)
	plan.Name = types.StringValue(fresh.Name)
	plan.Region = types.StringValue(region)
	plan.ProjectTag = types.StringValue(projectTag)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *S3BucketResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data S3BucketResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := &client.RequestOpts{}
	if !data.Region.IsNull() && !data.Region.IsUnknown() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() && !data.ProjectTag.IsUnknown() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}

	name := data.Name.ValueString()
	if name == "" {
		name = data.ID.ValueString()
	}

	b, err := r.c.GetBucket(ctx, name, opts)
	if err != nil {
		if client.IsNotFound(err) {
			tflog.Warn(ctx, "Bucket not found, removing from state", map[string]any{"name": name})
			resp.State.RemoveResource(ctx)
			return
		}
		// 712 is NOT IsNotFound — surface so caller sees state hasn't moved to
		// another project silently.
		resp.Diagnostics.AddError("Unable to Read Bucket", err.Error())
		return
	}

	if rfErr := r.refreshFromServer(ctx, &data, b, opts); rfErr != nil {
		resp.Diagnostics.AddError("Unable to Read Bucket configuration", rfErr.Error())
		return
	}
	data.ID = types.StringValue(b.Name)
	data.Name = types.StringValue(b.Name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *S3BucketResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var state, plan S3BucketResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := &client.RequestOpts{}
	if !state.Region.IsNull() {
		opts.Region = state.Region.ValueString()
	}
	if !state.ProjectTag.IsNull() {
		opts.ProjectTag = state.ProjectTag.ValueString()
	}
	name := state.Name.ValueString()

	if !state.Acl.Equal(plan.Acl) {
		if err := r.c.PutBucketAcl(ctx, name, client.PutBucketAclRequest{Acl: aclToEnum(plan.Acl.ValueString())}, opts); err != nil {
			resp.Diagnostics.AddError("Unable to Update Bucket ACL", err.Error())
			return
		}
	}
	if !state.Versioning.Equal(plan.Versioning) {
		vc := versioningToConfig(plan.Versioning.ValueString())
		if vc != nil {
			if err := r.c.PutBucketVersioning(ctx, name, client.PutBucketVersioningRequest{VersioningConfiguration: vc}, opts); err != nil {
				resp.Diagnostics.AddError("Unable to Update Bucket Versioning", err.Error())
				return
			}
		}
		// "disabled" target rejected by ModifyPlan; unreachable.
	}

	b, err := r.c.GetBucket(ctx, name, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Bucket after Update", err.Error())
		return
	}
	if rfErr := r.refreshFromServer(ctx, &plan, b, opts); rfErr != nil {
		resp.Diagnostics.AddError("Unable to Refresh Bucket configuration", rfErr.Error())
		return
	}
	plan.ID = types.StringValue(b.Name)
	plan.Name = types.StringValue(b.Name)
	plan.Region = state.Region
	plan.ProjectTag = state.ProjectTag
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *S3BucketResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data S3BucketResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	opts := &client.RequestOpts{}
	if !data.Region.IsNull() {
		opts.Region = data.Region.ValueString()
	}
	if !data.ProjectTag.IsNull() {
		opts.ProjectTag = data.ProjectTag.ValueString()
	}
	name := data.Name.ValueString()
	forceDestroy := data.ForceDestroy.ValueBool()

	tflog.Debug(ctx, "Deleting bucket", map[string]any{"name": name, "force_destroy": forceDestroy})

	err := r.c.DeleteBucket(ctx, name, forceDestroy, opts)
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Unable to Delete Bucket", err.Error())
		return
	}
}

func (r *S3BucketResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	region, name, projectTag, ok := parseImportID(req.ID)
	if !ok {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected `{region}/{name}@{project_tag}`, got: %q\n\n"+
				"Example: terraform import prodata_s3_bucket.example UZ-5/my-bucket@my-project", req.ID),
		)
		return
	}
	tflog.Info(ctx, "Importing S3 bucket", map[string]any{"region": region, "name": name, "project_tag": projectTag})
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("region"), region)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_tag"), projectTag)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
}

// refreshFromServer fills versioning + object_lock_enabled + creation_date on data
// from the live server state. Acl is intentionally NOT refreshed (trust-state).
func (r *S3BucketResource) refreshFromServer(ctx context.Context, data *S3BucketResourceModel, b *client.Bucket, opts *client.RequestOpts) error {
	if b.CreationDate != "" {
		data.CreationDate = types.StringValue(b.CreationDate)
	} else {
		data.CreationDate = types.StringNull()
	}
	vc, err := r.c.GetBucketVersioning(ctx, b.Name, opts)
	if err != nil {
		return fmt.Errorf("get versioning: %w", err)
	}
	data.Versioning = types.StringValue(versioningFromConfig(vc))

	olc, err := r.c.GetObjectLockConfiguration(ctx, b.Name, opts)
	if err != nil {
		return fmt.Errorf("get object-locking: %w", err)
	}
	data.ObjectLockEnabled = types.BoolValue(objectLockFromConfig(olc))
	return nil
}

// ---- enum + drift helpers ----

func aclToEnum(canonical string) string {
	switch canonical {
	case "private":
		return "PRIVATE"
	case "public-read":
		return "PUBLIC_READ"
	case "public-read-write":
		return "PUBLIC_READ_WRITE"
	default:
		return ""
	}
}

// versioningToConfig maps the canonical TF wire form to the panel DTO.
// "disabled" → nil (panel/S3 has no DISABLED status; omit the wrapper).
func versioningToConfig(canonical string) *client.VersioningConfiguration {
	switch canonical {
	case "enabled":
		return &client.VersioningConfiguration{Status: "ENABLED"}
	case "suspended":
		return &client.VersioningConfiguration{Status: "SUSPENDED"}
	default:
		return nil
	}
}

func versioningFromConfig(vc *client.VersioningConfiguration) string {
	if vc == nil {
		return "disabled"
	}
	switch vc.Status {
	case "ENABLED":
		return "enabled"
	case "SUSPENDED":
		return "suspended"
	default:
		return "disabled"
	}
}

func objectLockFromConfig(olc *client.ObjectLockConfiguration) bool {
	return olc != nil && olc.ObjectLockEnabled == "ENABLED"
}

// ---- plan-time validation helpers (extracted for unit testing) ----

var bucketNameRegex = regexp.MustCompile(`^[a-z0-9.-]+$`)

func validateBucketNameStr(name string) string {
	if l := len(name); l < 3 || l > 24 {
		return fmt.Sprintf("bucket name must be 3-24 characters, got %d", l)
	}
	if !bucketNameRegex.MatchString(name) {
		return "bucket name may only contain lowercase letters, digits, dots and hyphens"
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "-") ||
		strings.HasSuffix(name, ".") || strings.HasSuffix(name, "-") {
		return "bucket name must start and end with a letter or digit"
	}
	for _, pair := range []string{"..", "--", ".-", "-."} {
		if strings.Contains(name, pair) {
			return "bucket name must not contain consecutive dots or hyphens"
		}
	}
	return ""
}

func validateObjectLockRequiresVersioning(objectLockEnabled bool, versioning string) string {
	if objectLockEnabled && versioning != "enabled" {
		return `object_lock_enabled = true requires versioning = "enabled"`
	}
	return ""
}

// validateVersioningTransition enforces FR-8: once versioning has been ENABLED or
// SUSPENDED server-side, the only legal next state is the other of those two — never
// back to disabled (S3 has no DISABLED state).
func validateVersioningTransition(prior, next string) string {
	if (prior == "enabled" || prior == "suspended") && next == "disabled" {
		return fmt.Sprintf("versioning cannot transition from %q back to %q (S3 does not support disabling versioning)", prior, next)
	}
	return ""
}

func parseImportID(s string) (region, name, projectTag string, ok bool) {
	slash := strings.IndexByte(s, '/')
	at := strings.LastIndexByte(s, '@')
	if slash <= 0 || at <= slash+1 || at >= len(s)-1 {
		return
	}
	region = s[:slash]
	name = s[slash+1 : at]
	projectTag = s[at+1:]
	if region == "" || name == "" || projectTag == "" {
		return
	}
	return region, name, projectTag, true
}

// ---- bucketNameValidator wraps validateBucketNameStr for the framework. ----

type bucketNameValidator struct{}

func (bucketNameValidator) Description(context.Context) string {
	return "valid S3 bucket name (3-24 chars, lowercase letters/digits/.-; no consecutive separators; no leading/trailing separators)"
}

func (v bucketNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v bucketNameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if msg := validateBucketNameStr(req.ConfigValue.ValueString()); msg != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid bucket name", msg)
	}
}
