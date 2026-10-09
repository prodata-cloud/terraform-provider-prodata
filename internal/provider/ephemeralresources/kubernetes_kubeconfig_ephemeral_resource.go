// Package ephemeralresources holds the provider's ephemeral resources: values that
// exist only for the duration of a plan or apply and are never written to state or
// to a saved plan. Ephemeral resources need Terraform 1.10 or later.
package ephemeralresources

import (
	"context"
	"fmt"
	"time"

	"terraform-provider-prodata/internal/client"
	"terraform-provider-prodata/internal/tfutil"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ ephemeral.EphemeralResource              = &K8sKubeconfigEphemeralResource{}
	_ ephemeral.EphemeralResourceWithConfigure = &K8sKubeconfigEphemeralResource{}
)

// openTimeout bounds the one API call Open makes. The API client has no HTTP timeout on
// purpose and Terraform gives an ephemeral resource's context no deadline, so without
// this a hung panel would hang `terraform plan` for every configuration that uses it.
var openTimeout = 2 * time.Minute

// K8sKubeconfigEphemeralResource implements the prodata_kubernetes_kubeconfig ephemeral
// resource: a cluster's connection details, held in memory only.
type K8sKubeconfigEphemeralResource struct {
	c *client.Client
}

// K8sKubeconfigModel mirrors the prodata_kubernetes_kubeconfig schema. The connection
// fields are the flat counterpart of the kube_config object on the cluster resource and
// data source, with the same names and encodings, so moving a configuration over is a
// change of reference only.
type K8sKubeconfigModel struct {
	// Lookup — all three are config inputs and are echoed back unchanged.
	ClusterID  types.Int64  `tfsdk:"cluster_id"`
	Region     types.String `tfsdk:"region"`
	ProjectTag types.String `tfsdk:"project_tag"`

	// Computed connection details.
	Host                 types.String `tfsdk:"host"`
	ClusterCACertificate types.String `tfsdk:"cluster_ca_certificate"`
	ClientCertificate    types.String `tfsdk:"client_certificate"`
	ClientKey            types.String `tfsdk:"client_key"`
	Token                types.String `tfsdk:"token"`
	RawConfig            types.String `tfsdk:"raw_config"`
}

func NewK8sKubeconfigEphemeralResource() ephemeral.EphemeralResource {
	return &K8sKubeconfigEphemeralResource{}
}

func (e *K8sKubeconfigEphemeralResource) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_kubeconfig"
}

func (e *K8sKubeconfigEphemeralResource) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Reads the connection details of a ProData Managed Kubernetes cluster — API server " +
			"address, CA certificate, client certificate and key, token, and the full kubeconfig — **without " +
			"writing them to Terraform state or to a saved plan**. Use it to configure the `kubernetes` and " +
			"`helm` providers. Requires Terraform 1.10 or later. The lookup fails while the panel has not " +
			"produced a kubeconfig for the cluster, which it does once the cluster is ready.",
		Attributes: map[string]schema.Attribute{
			"cluster_id": schema.Int64Attribute{
				MarkdownDescription: "ID of the Kubernetes cluster, for example `prodata_kubernetes_cluster.main.id`.",
				Required:            true,
				Validators:          []validator.Int64{int64validator.AtLeast(1)},
			},
			"region": schema.StringAttribute{
				MarkdownDescription: "Region ID override. If omitted, uses the provider default.",
				Optional:            true,
			},
			"project_tag": schema.StringAttribute{
				MarkdownDescription: "Project tag override. If omitted, uses the provider default.",
				Optional:            true,
			},
			// Every connection attribute is Sensitive, like the kube_config object they mirror:
			// a sensitive value is redacted wherever Terraform would print it.
			"host": schema.StringAttribute{
				MarkdownDescription: "Kubernetes API server URL.",
				Computed:            true,
				Sensitive:           true,
			},
			"cluster_ca_certificate": schema.StringAttribute{
				MarkdownDescription: "Base64-encoded cluster CA certificate, exactly as it appears in the kubeconfig. " +
					"Wrap it in `base64decode()` when passing it to the kubernetes provider.",
				Computed:  true,
				Sensitive: true,
			},
			"client_certificate": schema.StringAttribute{
				MarkdownDescription: "Base64-encoded client certificate for cluster-admin access. Null when the " +
					"kubeconfig has none (token auth). Wrap it in `base64decode()` when passing it to the " +
					"kubernetes provider.",
				Computed:  true,
				Sensitive: true,
			},
			"client_key": schema.StringAttribute{
				MarkdownDescription: "Base64-encoded client key for cluster-admin access. Null when the kubeconfig " +
					"has none (token auth). Wrap it in `base64decode()` when passing it to the kubernetes provider.",
				Computed:  true,
				Sensitive: true,
			},
			"token": schema.StringAttribute{
				MarkdownDescription: "Bearer token, when the cluster uses token auth. Null otherwise.",
				Computed:            true,
				Sensitive:           true,
			},
			"raw_config": schema.StringAttribute{
				MarkdownDescription: "The full kubeconfig as plain YAML.",
				Computed:            true,
				Sensitive:           true,
			},
		},
	}
}

func (e *K8sKubeconfigEphemeralResource) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Ephemeral Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData))
		return
	}
	e.c = c
}

// Open reads the cluster and returns its connection details.
//
// It fails rather than return nulls whenever usable credentials are not available. The
// result is meant to feed a provider block, and a kubernetes/helm provider that is given
// no host or no credentials does not stop: it falls back to whatever other connection
// settings it can find (a kubeconfig named by config_path or KUBE_CONFIG_PATH, the
// service account of the pod it runs in) and talks to the cluster those name. A clear
// error here is the safe outcome.
//
// It cannot cover a provider that is handed a kubeconfig file as well: the explicit values
// are laid over that file and whatever is left unset (a token, an exec plugin) is taken
// from it. The docs tell users not to configure one.
func (e *K8sKubeconfigEphemeralResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var data K8sKubeconfigModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if e.c == nil {
		resp.Diagnostics.AddError("Provider is not configured",
			"The ProData provider has not been configured, so the cluster cannot be read.")
		return
	}
	// Terraform opens an ephemeral resource only once its configuration is fully known;
	// this is a guard against calling the API for cluster 0 should that ever change.
	if data.ClusterID.IsNull() || data.ClusterID.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("cluster_id"), "cluster_id is not known",
			"cluster_id must have a value when the kubeconfig is read.")
		return
	}
	id := data.ClusterID.ValueInt64()

	tflog.Debug(ctx, "Opening Kubernetes kubeconfig", map[string]any{"cluster_id": id})

	ctx, cancel := context.WithTimeout(ctx, openTimeout)
	defer cancel()

	cl, err := e.c.GetCluster(ctx, id, scopeOpts(data.Region, data.ProjectTag))
	if err != nil {
		if client.IsKuberNotFound(err) {
			resp.Diagnostics.AddError("Kubernetes cluster not found",
				fmt.Sprintf("Cluster %d was not found in this region and project.", id))
			return
		}
		resp.Diagnostics.AddError("Unable to read Kubernetes cluster", client.KuberErrorDetail(err))
		return
	}
	// A soft-deleted cluster reads DELETED forever (there is no 404).
	if cl.Status == client.ClusterStatusDeleted {
		resp.Diagnostics.AddError("Kubernetes cluster is deleted",
			fmt.Sprintf("Cluster %d has been deleted.", id))
		return
	}

	// Diagnostics below name the cluster and its status only: nothing derived from the
	// kubeconfig may ever appear in a message.
	kc := client.ParseKubeConfig(cl.Kubeconfig)
	if kc == nil {
		resp.Diagnostics.AddError("Kubeconfig is not available",
			fmt.Sprintf("Cluster %d has status %s and the panel has not produced a kubeconfig for it. "+
				"The kubeconfig appears once the cluster reaches SUCCESS (the panel fetches it lazily, so "+
				"it can lag briefly). Wait until the cluster is ready and run the operation again.",
				id, cl.Status))
		return
	}
	// ParseKubeConfig is lenient and still returns the raw text when it cannot read the
	// connection fields. Without an API server address the credentials cannot be used,
	// and handing back a null host would be the unsafe case described on Open.
	if kc.Host == "" {
		resp.Diagnostics.AddError("Kubeconfig could not be used",
			fmt.Sprintf("The panel returned a kubeconfig for cluster %d, but no API server address could be "+
				"read from it, so it cannot be used to connect. Contact ProData support.", id))
		return
	}
	// The same holds for the credentials: an address alone is the unsafe case again. The
	// parser leaves them empty when the current-context's user is missing, or when the user
	// authenticates some other way (a certificate file, an exec plugin), and nulls in
	// client_certificate, client_key and token would let the consuming provider add
	// credentials of its own.
	if !hasUsableCredentials(kc) {
		resp.Diagnostics.AddError("Kubeconfig could not be used",
			fmt.Sprintf("The panel returned a kubeconfig for cluster %d, but it holds neither a client "+
				"certificate with its key nor a token, so it cannot be used to authenticate. Contact "+
				"ProData support.", id))
		return
	}

	data.Host = types.StringValue(kc.Host)
	data.ClusterCACertificate = tfutil.StringOrNull(kc.ClusterCACertificate)
	data.ClientCertificate = tfutil.StringOrNull(kc.ClientCertificate)
	data.ClientKey = tfutil.StringOrNull(kc.ClientKey)
	data.Token = tfutil.StringOrNull(kc.Token)
	data.RawConfig = tfutil.StringOrNull(kc.Raw)

	resp.Diagnostics.Append(resp.Result.Set(ctx, &data)...)
}

// hasUsableCredentials reports whether the kubeconfig carries a way to authenticate: a
// client certificate together with its key, or a bearer token. Half of a pair does not count.
func hasUsableCredentials(kc *client.KubeConfig) bool {
	return (kc.ClientCertificate != "" && kc.ClientKey != "") || kc.Token != ""
}

// scopeOpts builds a RequestOpts carrying only the region / project_tag overrides that
// are actually set; an empty field defers to the provider/client default.
func scopeOpts(region, projectTag types.String) *client.RequestOpts {
	opts := &client.RequestOpts{}
	if !region.IsNull() && !region.IsUnknown() && region.ValueString() != "" {
		opts.Region = region.ValueString()
	}
	if !projectTag.IsNull() && !projectTag.IsUnknown() && projectTag.ValueString() != "" {
		opts.ProjectTag = projectTag.ValueString()
	}
	return opts
}
