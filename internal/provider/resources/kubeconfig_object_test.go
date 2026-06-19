package resources

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// TestK8sClusterKubeConfigUnknownDecodes reproduces the create-time plan, where
// the Computed kube_config object is unknown and everything else null. Get must
// decode it without error — which only works because the model field is a
// types.Object (a *struct field cannot hold an unknown value). Regression guard
// for the original *K8sKubeConfigModel design.
func TestK8sClusterKubeConfigUnknownDecodes(t *testing.T) {
	ctx := context.Background()
	var resp resource.SchemaResponse
	NewK8sClusterResource().(*K8sClusterResource).Schema(ctx, resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}

	objType := resp.Schema.Type().TerraformType(ctx).(tftypes.Object)
	attrs := map[string]tftypes.Value{}
	for name, aTyp := range objType.AttributeTypes {
		if name == "kube_config" {
			attrs[name] = tftypes.NewValue(aTyp, tftypes.UnknownValue)
		} else {
			attrs[name] = tftypes.NewValue(aTyp, nil)
		}
	}
	raw := tftypes.NewValue(objType, attrs)

	st := tfsdk.State{Schema: resp.Schema, Raw: raw}
	var m K8sClusterModel
	if diags := st.Get(ctx, &m); diags.HasError() {
		t.Fatalf("Get with unknown kube_config failed: %v", diags)
	}
	if !m.KubeConfig.IsUnknown() {
		t.Errorf("kube_config = %v, want unknown", m.KubeConfig)
	}
}

// TestKubeConfigObject verifies the builder: an empty secret yields a null object,
// and a valid kubeconfig yields a known object whose host is parsed. A known
// object also proves kubeConfigAttrTypes() and K8sKubeConfigModel have not drifted
// (ObjectValueFrom would otherwise error and fail safe to null).
func TestKubeConfigObject(t *testing.T) {
	ctx := context.Background()

	if o := kubeConfigObject(ctx, ""); !o.IsNull() {
		t.Errorf("empty secret: want null object, got %v", o)
	}

	secret := base64.StdEncoding.EncodeToString([]byte(`apiVersion: v1
current-context: c
clusters:
- name: c
  cluster:
    server: https://h:6443
    certificate-authority-data: Q0E=
users:
- name: u
  user:
    client-certificate-data: Q0M=
    client-key-data: Q0s=
contexts:
- name: c
  context:
    cluster: c
    user: u
`))
	o := kubeConfigObject(ctx, secret)
	if o.IsNull() || o.IsUnknown() {
		t.Fatalf("valid secret: want known object, got null=%v unknown=%v", o.IsNull(), o.IsUnknown())
	}
	host, ok := o.Attributes()["host"].(types.String)
	if !ok || host.ValueString() != "https://h:6443" {
		t.Errorf("host attr = %v, want https://h:6443", o.Attributes()["host"])
	}
}
