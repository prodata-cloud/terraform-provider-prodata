package resources

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                   = &SecurityGroupResource{}
	_ resource.ResourceWithConfigure      = &SecurityGroupResource{}
	_ resource.ResourceWithImportState    = &SecurityGroupResource{}
	_ resource.ResourceWithValidateConfig = &SecurityGroupResource{}
)

type SecurityGroupResource struct{ c *client.Client }

type SecurityGroupModel struct {
	ID         types.Int64  `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`
	Rules      types.List   `tfsdk:"rules"`
}

type securityGroupRuleModel struct {
	Direction       types.String `tfsdk:"direction"`
	Action          types.String `tfsdk:"action"`
	Protocol        types.String `tfsdk:"protocol"`
	Source          types.String `tfsdk:"source"`
	Destination     types.String `tfsdk:"destination"`
	SourcePort      types.String `tfsdk:"source_port"`
	DestinationPort types.String `tfsdk:"destination_port"`
}

func NewSecurityGroupResource() resource.Resource { return &SecurityGroupResource{} }

func (r *SecurityGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group"
}

func securityGroupScopeAttribute(description string) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: description, Optional: true, Computed: true,
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace(), stringplanmodifier.UseStateForUnknown()}}
}

func securityGroupOptionalRule(description string, validators ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: description, Optional: true, Computed: true,
		Default: stringdefault.StaticString(""), Validators: validators}
}

func (r *SecurityGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a security group and its complete ordered ruleset. Rule changes and renames update in place. Requires the security-groups v2 API.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.Int64Attribute{Computed: true, MarkdownDescription: "Security group ID.", PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}},
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Name, unique within the company.", Validators: []validator.String{stringvalidator.LengthBetween(1, 100), stringvalidator.RegexMatches(regexp.MustCompile(`\S`), "Name must not be blank.")}},
			"region":      securityGroupScopeAttribute("Region context. Defaults to the provider region; changing it replaces the resource."),
			"project_tag": securityGroupScopeAttribute("Owning project. Defaults to the provider project; changing it replaces the resource."),
			"rules": schema.ListNestedAttribute{Required: true, MarkdownDescription: "Complete ordered ruleset, evaluated from first to last within each direction. Use [] for an empty group. Do not also manage these rules outside this resource.", Validators: []validator.List{listvalidator.SizeAtMost(200)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"direction":        schema.StringAttribute{Required: true, MarkdownDescription: "Traffic direction: in or out.", Validators: []validator.String{stringvalidator.OneOf("in", "out")}},
					"action":           schema.StringAttribute{Required: true, MarkdownDescription: "ACCEPT, DROP, or REJECT.", Validators: []validator.String{stringvalidator.OneOf("ACCEPT", "DROP", "REJECT")}},
					"protocol":         securityGroupOptionalRule("tcp, udp, icmp, ipv6-icmp, or empty for any protocol.", stringvalidator.OneOf("", "tcp", "udp", "icmp", "ipv6-icmp")),
					"source":           securityGroupOptionalRule("Source IP or CIDR; empty matches any source.", securityGroupAddressValidator{}),
					"destination":      securityGroupOptionalRule("Destination IP or CIDR; empty matches any destination.", securityGroupAddressValidator{}),
					"source_port":      securityGroupOptionalRule("TCP/UDP source ports: a port, colon-separated range, or comma-separated list; empty matches any.", securityGroupPortValidator{}),
					"destination_port": securityGroupOptionalRule("TCP/UDP destination ports: a port, colon-separated range, or comma-separated list; empty matches any.", securityGroupPortValidator{}),
				}}},
		},
	}
}

func (r *SecurityGroupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.c, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data", fmt.Sprintf("Expected *client.Client, got %T.", req.ProviderData))
	}
}

func (m *SecurityGroupModel) scope(c *client.Client) *client.RequestOpts {
	if m.Region.IsNull() || m.Region.IsUnknown() || m.Region.ValueString() == "" {
		m.Region = types.StringValue(c.Region)
	}
	if m.ProjectTag.IsNull() || m.ProjectTag.IsUnknown() || m.ProjectTag.ValueString() == "" {
		m.ProjectTag = types.StringValue(c.ProjectTag)
	}
	return &client.RequestOpts{Region: m.Region.ValueString(), ProjectTag: m.ProjectTag.ValueString(), Lang: "en"}
}

func (m SecurityGroupModel) request(ctx context.Context) (client.SecurityGroupRequest, diag.Diagnostics) {
	var models []securityGroupRuleModel
	diags := m.Rules.ElementsAs(ctx, &models, false)
	rules := make([]client.SecurityGroupRule, 0, len(models))
	for _, rule := range models {
		rules = append(rules, client.SecurityGroupRule{Direction: rule.Direction.ValueString(), Action: rule.Action.ValueString(),
			Protocol: rule.Protocol.ValueString(), Source: rule.Source.ValueString(), Destination: rule.Destination.ValueString(),
			SourcePort: rule.SourcePort.ValueString(), DestinationPort: rule.DestinationPort.ValueString()})
	}
	return client.SecurityGroupRequest{Name: m.Name.ValueString(), Rules: rules}, diags
}

var securityGroupRuleTypes = map[string]attr.Type{
	"direction": types.StringType, "action": types.StringType, "protocol": types.StringType,
	"source": types.StringType, "destination": types.StringType, "source_port": types.StringType, "destination_port": types.StringType,
}

func (m *SecurityGroupModel) set(ctx context.Context, result *client.SecurityGroup) diag.Diagnostics {
	var diags diag.Diagnostics
	if result.ID <= 0 {
		diags.AddError("Invalid Security Group Response", "The API returned no valid security group ID.")
		return diags
	}
	m.ID = types.Int64Value(result.ID)
	m.Name = types.StringValue(result.Name)
	rules := make([]securityGroupRuleModel, 0, len(result.Rules))
	for _, rule := range result.Rules {
		rules = append(rules, securityGroupRuleModel{Direction: types.StringValue(rule.Direction), Action: types.StringValue(rule.Action),
			Protocol: types.StringValue(rule.Protocol), Source: types.StringValue(rule.Source), Destination: types.StringValue(rule.Destination),
			SourcePort: types.StringValue(rule.SourcePort), DestinationPort: types.StringValue(rule.DestinationPort)})
	}
	m.Rules, diags = types.ListValueFrom(ctx, types.ObjectType{AttrTypes: securityGroupRuleTypes}, rules)
	return diags
}

func (r *SecurityGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m SecurityGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	opts := m.scope(r.c)
	body, diags := m.request(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := client.RetryOnBusy(ctx, client.RetryTimeoutShort, func() (*client.SecurityGroup, error) { return r.c.CreateSecurityGroup(ctx, body, opts) })
	if err != nil {
		resp.Diagnostics.AddError("Unable to Create Security Group", err.Error())
		return
	}
	resp.Diagnostics.Append(m.set(ctx, result)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *SecurityGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m SecurityGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.c.GetSecurityGroup(ctx, m.ID.ValueInt64(), m.scope(r.c))
	if err != nil {
		if client.IsSecurityGroupNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read Security Group", err.Error())
		return
	}
	if result.ID != m.ID.ValueInt64() {
		resp.Diagnostics.AddError("Invalid Security Group Response", "The API returned a different group ID; state was preserved.")
		return
	}
	resp.Diagnostics.Append(m.set(ctx, result)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *SecurityGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var m, prior SecurityGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, diags := m.request(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := client.RetryOnBusy(ctx, client.RetryTimeoutShort, func() (*client.SecurityGroup, error) {
		return r.c.UpdateSecurityGroup(ctx, prior.ID.ValueInt64(), body, m.scope(r.c))
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to Update Security Group", err.Error())
		return
	}
	if result.ID != prior.ID.ValueInt64() {
		resp.Diagnostics.AddError("Invalid Security Group Response", "The API returned a different group ID; state was preserved.")
		return
	}
	resp.Diagnostics.Append(m.set(ctx, result)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
}

func (r *SecurityGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m SecurityGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := client.RetryVoidOnBusy(ctx, client.RetryTimeoutShort, func() error { return r.c.DeleteSecurityGroup(ctx, m.ID.ValueInt64(), m.scope(r.c)) })
	if err != nil && !client.IsSecurityGroupNotFound(err) {
		resp.Diagnostics.AddError("Unable to Delete Security Group", err.Error())
	}
}

func (r *SecurityGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := strconv.ParseInt(req.ID, 10, 64)
	if err != nil || id <= 0 {
		resp.Diagnostics.AddError("Invalid Import ID", "Expected a positive security group ID.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
