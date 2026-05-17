package datasources

import (
	"context"
	"fmt"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ datasource.DataSource              = &S3BucketDataSource{}
	_ datasource.DataSourceWithConfigure = &S3BucketDataSource{}
)

type S3BucketDataSource struct {
	c *client.Client
}

type S3BucketDataSourceModel struct {
	ID                types.String `tfsdk:"id"`
	Region            types.String `tfsdk:"region"`
	ProjectTag        types.String `tfsdk:"project_tag"`
	Name              types.String `tfsdk:"name"`
	CreationDate      types.String `tfsdk:"creation_date"`
	Versioning        types.Bool   `tfsdk:"versioning"`
	ObjectLockEnabled types.Bool   `tfsdk:"object_lock_enabled"`
	Size              types.Int64  `tfsdk:"size"`
	ObjectCount       types.Int64  `tfsdk:"object_count"`
}

func NewS3BucketDataSource() datasource.DataSource {
	return &S3BucketDataSource{}
}

func (d *S3BucketDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_s3_bucket"
}

func (d *S3BucketDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Look up a single ProData S3 (Ceph RGW) bucket by name. " +
			"Scoped to the project specified via `project_tag` (or provider default). " +
			"Errors if the bucket does not exist or belongs to another project. " +
			"`acl` is intentionally not exposed (canned ACL does not round-trip from S3 grants); " +
			"use the resource if you need to manage ACL.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Equal to `name`.",
				Computed:            true,
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If omitted, uses provider default.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag override. If omitted, uses provider default.",
				Optional:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Bucket name to look up.",
				Required:            true,
			},
			"creation_date": schema.StringAttribute{
				MarkdownDescription: "Server-reported bucket creation timestamp (ISO-8601).",
				Computed:            true,
			},
			"versioning": schema.BoolAttribute{
				MarkdownDescription: "Whether object versioning is enabled. `true` only when the " +
					"bucket's versioning state is ENABLED; a suspended or never-configured bucket reads as `false`.",
				Computed: true,
			},
			"object_lock_enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether S3 object lock is enabled on the bucket.",
				Computed:            true,
			},
			"size": schema.Int64Attribute{
				MarkdownDescription: "Total size in bytes of all objects currently stored in the bucket.",
				Computed:            true,
			},
			"object_count": schema.Int64Attribute{
				MarkdownDescription: "Number of objects currently stored in the bucket.",
				Computed:            true,
			},
		},
	}
}

func (d *S3BucketDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	d.c = c
}

func (d *S3BucketDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data S3BucketDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
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
	tflog.Debug(ctx, "Reading S3 bucket", map[string]any{
		"name":        name,
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	b, err := d.c.GetBucket(ctx, name, opts)
	if err != nil {
		// 712 (cross-project) is surfaced as an error, NOT silently as no-data — same
		// rule as the resource Read (no silent state drop / no silent empty result for
		// someone else's bucket with the same name).
		resp.Diagnostics.AddError("Unable to Read S3 Bucket", err.Error())
		return
	}

	data.ID = types.StringValue(b.Name)
	data.Name = types.StringValue(b.Name)
	if b.CreationDate != "" {
		data.CreationDate = types.StringValue(b.CreationDate)
	} else {
		data.CreationDate = types.StringNull()
	}
	if b.Size != nil {
		data.Size = types.Int64Value(*b.Size)
	} else {
		data.Size = types.Int64Value(0)
	}
	if b.ObjectCount != nil {
		data.ObjectCount = types.Int64Value(*b.ObjectCount)
	} else {
		data.ObjectCount = types.Int64Value(0)
	}

	vc, err := d.c.GetBucketVersioning(ctx, name, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Bucket Versioning", err.Error())
		return
	}
	data.Versioning = types.BoolValue(versioningFromConfig(vc))

	olc, err := d.c.GetObjectLockConfiguration(ctx, name, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to Read Bucket Object Lock", err.Error())
		return
	}
	data.ObjectLockEnabled = types.BoolValue(objectLockFromConfig(olc))

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
