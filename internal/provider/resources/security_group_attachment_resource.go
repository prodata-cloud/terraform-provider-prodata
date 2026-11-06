package resources

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	_ resource.Resource                = &SecurityGroupAttachmentResource{}
	_ resource.ResourceWithConfigure   = &SecurityGroupAttachmentResource{}
	_ resource.ResourceWithImportState = &SecurityGroupAttachmentResource{}
)

type SecurityGroupAttachmentResource struct{ c *client.Client }

type SecurityGroupAttachmentModel struct {
	ID          types.String `tfsdk:"id"`
	GroupID     types.Int64  `tfsdk:"security_group_id"`
	VmID        types.Int64  `tfsdk:"vm_id"`
	InterfaceID types.Int64  `tfsdk:"interface_id"`
	Region      types.String `tfsdk:"region"`
	ProjectTag  types.String `tfsdk:"project_tag"`
}

func NewSecurityGroupAttachmentResource() resource.Resource {
	return &SecurityGroupAttachmentResource{}
}

func (r *SecurityGroupAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_security_group_attachment"
}

func (r *SecurityGroupAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	id := func(description string) schema.Int64Attribute {
		return schema.Int64Attribute{Required: true, MarkdownDescription: description,
			Validators:    []validator.Int64{int64validator.AtLeast(1)},
			PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Binds a security group to one VM interface. The VM and group must belong to the selected project. Only one group may manage an interface. Destroy detaches only the specified group's binding.",
		Attributes: map[string]schema.Attribute{
			"id":                schema.StringAttribute{Computed: true, MarkdownDescription: "Composite group_id/vm_id/interface_id identifier.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"security_group_id": id("Security group ID. The group must contain at least one rule."),
			"vm_id":             id("Platform VM ID."),
			"interface_id":      id("Stable VM interface row ID (not the net0/net1 index)."),
			"region":            securityGroupScopeAttribute("Region context. Defaults to the provider region; changing it replaces the resource."),
			"project_tag":       securityGroupScopeAttribute("Project shared by the VM and group. Defaults to the provider project; changing it replaces the resource."),
		},
	}
}

func (r *SecurityGroupAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	var ok bool
	r.c, ok = req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data", fmt.Sprintf("Expected *client.Client, got %T.", req.ProviderData))
	}
}

func (m *SecurityGroupAttachmentModel) scope(c *client.Client) *client.RequestOpts {
	if m.Region.IsNull() || m.Region.IsUnknown() || m.Region.ValueString() == "" {
		m.Region = types.StringValue(c.Region)
	}
	if m.ProjectTag.IsNull() || m.ProjectTag.IsUnknown() || m.ProjectTag.ValueString() == "" {
		m.ProjectTag = types.StringValue(c.ProjectTag)
	}
	return &client.RequestOpts{Region: m.Region.ValueString(), ProjectTag: m.ProjectTag.ValueString(), Lang: "en"}
}

func (m SecurityGroupAttachmentModel) matches(result *client.SecurityGroupAttachment) bool {
	return result.GroupID == m.GroupID.ValueInt64() && result.VmID == m.VmID.ValueInt64() && result.InterfaceID == m.InterfaceID.ValueInt64()
}

func (m *SecurityGroupAttachmentModel) identity() {
	m.ID = types.StringValue(fmt.Sprintf("%d/%d/%d", m.GroupID.ValueInt64(), m.VmID.ValueInt64(), m.InterfaceID.ValueInt64()))
}

func (r *SecurityGroupAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m SecurityGroupAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	opts := m.scope(r.c)
	result, err := client.RetryOnBusy(ctx, client.RetryTimeoutShort, func() (*client.SecurityGroupAttachment, error) {
		return r.c.AttachSecurityGroup(ctx, m.GroupID.ValueInt64(), m.VmID.ValueInt64(), m.InterfaceID.ValueInt64(), opts)
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to Attach Security Group", err.Error())
		return
	}
	if !m.matches(result) {
		resp.Diagnostics.AddError("Invalid Attachment Response", "The API returned a different binding.")
		return
	}
	m.identity()
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *SecurityGroupAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m SecurityGroupAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	result, err := r.c.GetSecurityGroupAttachment(ctx, m.GroupID.ValueInt64(), m.VmID.ValueInt64(), m.InterfaceID.ValueInt64(), m.scope(r.c))
	if err != nil {
		if client.IsSecurityGroupNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to Read Security Group Attachment", err.Error())
		return
	}
	if !m.matches(result) {
		resp.Diagnostics.AddError("Invalid Attachment Response", "The API returned a different binding; state was preserved.")
		return
	}
	m.identity()
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

func (r *SecurityGroupAttachmentResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Unexpected Attachment Update", "All attachment changes require replacement.")
}

func (r *SecurityGroupAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m SecurityGroupAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := client.RetryVoidOnBusy(ctx, client.RetryTimeoutShort, func() error {
		return r.c.DetachSecurityGroup(ctx, m.GroupID.ValueInt64(), m.VmID.ValueInt64(), m.InterfaceID.ValueInt64(), m.scope(r.c))
	})
	if err != nil && !client.IsSecurityGroupNotFound(err) {
		resp.Diagnostics.AddError("Unable to Detach Security Group", err.Error())
	}
}

func (r *SecurityGroupAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 {
		resp.Diagnostics.AddError("Invalid Import ID", "Expected group_id/vm_id/interface_id, all positive integers.")
		return
	}
	values := make([]int64, 3)
	for i, part := range parts {
		value, err := strconv.ParseInt(part, 10, 64)
		if err != nil || value <= 0 {
			resp.Diagnostics.AddError("Invalid Import ID", "Expected group_id/vm_id/interface_id, all positive integers.")
			return
		}
		values[i] = value
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), fmt.Sprintf("%d/%d/%d", values[0], values[1], values[2]))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("security_group_id"), values[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vm_id"), values[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("interface_id"), values[2])...)
}
