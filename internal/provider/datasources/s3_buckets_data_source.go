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
	_ datasource.DataSource              = &S3BucketsDataSource{}
	_ datasource.DataSourceWithConfigure = &S3BucketsDataSource{}
)

type S3BucketsDataSource struct {
	c *client.Client
}

type S3BucketsDataSourceModel struct {
	Region     types.String      `tfsdk:"region"`
	ProjectTag types.String      `tfsdk:"project_tag"`
	Names      []types.String    `tfsdk:"names"`
	Buckets    []S3BucketSummary `tfsdk:"buckets"`
}

// S3BucketSummary mirrors the list-call response shape. Versioning and
// object-lock are NOT fetched per bucket — that would be O(N) extra round-trips
// per list refresh. Use the `prodata_s3_bucket` data source if you need them.
type S3BucketSummary struct {
	Name         types.String `tfsdk:"name"`
	CreationDate types.String `tfsdk:"creation_date"`
	Size         types.Int64  `tfsdk:"size"`
	ObjectCount  types.Int64  `tfsdk:"object_count"`
}

func NewS3BucketsDataSource() datasource.DataSource {
	return &S3BucketsDataSource{}
}

func (d *S3BucketsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_s3_buckets"
}

func (d *S3BucketsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "List all S3 (Ceph RGW) buckets owned by the project. " +
			"Pagination is handled internally — the provider follows `continuationToken` " +
			"until the server stops returning one. Returns each bucket's name, creation date, " +
			"size, and object count; per-bucket versioning / object-lock are NOT fetched " +
			"(use the `prodata_s3_bucket` data source for those).",
		Attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If omitted, uses provider default.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag override. If omitted, uses provider default.",
				Optional:            true,
			},
			"names": schema.ListAttribute{
				MarkdownDescription: "Convenience list of just the bucket names, in the order returned by the server.",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"buckets": schema.ListNestedAttribute{
				MarkdownDescription: "Buckets owned by the project.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							MarkdownDescription: "Bucket name.",
							Computed:            true,
						},
						"creation_date": schema.StringAttribute{
							MarkdownDescription: "Server-reported bucket creation timestamp (ISO-8601).",
							Computed:            true,
						},
						"size": schema.Int64Attribute{
							MarkdownDescription: "Total size in bytes of all objects in the bucket.",
							Computed:            true,
						},
						"object_count": schema.Int64Attribute{
							MarkdownDescription: "Number of objects currently stored in the bucket.",
							Computed:            true,
						},
					},
				},
			},
		},
	}
}

func (d *S3BucketsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *S3BucketsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data S3BucketsDataSourceModel
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

	tflog.Debug(ctx, "Listing S3 buckets", map[string]any{
		"region":      opts.Region,
		"project_tag": opts.ProjectTag,
	})

	// pageSize=0 lets the server pick its default; ListBuckets follows continuationToken until empty.
	buckets, err := d.c.ListBuckets(ctx, 0, opts)
	if err != nil {
		resp.Diagnostics.AddError("Unable to List S3 Buckets", err.Error())
		return
	}

	data.Buckets = make([]S3BucketSummary, len(buckets))
	data.Names = make([]types.String, len(buckets))
	for i, b := range buckets {
		entry := S3BucketSummary{
			Name: types.StringValue(b.Name),
		}
		if b.CreationDate != "" {
			entry.CreationDate = types.StringValue(b.CreationDate)
		} else {
			entry.CreationDate = types.StringNull()
		}
		if b.Size != nil {
			entry.Size = types.Int64Value(*b.Size)
		} else {
			entry.Size = types.Int64Value(0)
		}
		if b.ObjectCount != nil {
			entry.ObjectCount = types.Int64Value(*b.ObjectCount)
		} else {
			entry.ObjectCount = types.Int64Value(0)
		}
		data.Buckets[i] = entry
		data.Names[i] = types.StringValue(b.Name)
	}

	tflog.Debug(ctx, "Listed S3 buckets", map[string]any{"count": len(buckets)})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
