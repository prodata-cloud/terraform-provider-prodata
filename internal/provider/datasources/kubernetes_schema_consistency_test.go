package datasources

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

// assertAttrTypesMatch checks that an attr.Type map has exactly the keys of the
// nested schema.
func assertAttrTypesMatch(t *testing.T, level string, attrTypes map[string]attr.Type, attrs map[string]dschema.Attribute) {
	t.Helper()
	for k := range attrTypes {
		if _, ok := attrs[k]; !ok {
			t.Errorf("[%s] attr-type key %q has no matching schema attribute", level, k)
		}
	}
	for k := range attrs {
		if _, ok := attrTypes[k]; !ok {
			t.Errorf("[%s] schema attribute %q has no matching attr-type key", level, k)
		}
	}
}

// These guard each k8s data-source schema against tfsdk-tag / attribute-key drift,
// which the framework only catches at Read time (see the resources package's
// equivalent test for the rationale).

func modelTags(modelType reflect.Type) map[string]bool {
	tags := map[string]bool{}
	for i := 0; i < modelType.NumField(); i++ {
		if tag, ok := modelType.Field(i).Tag.Lookup("tfsdk"); ok {
			tags[tag] = true
		}
	}
	return tags
}

func assertTagsMatch(t *testing.T, level string, modelType reflect.Type, attrs map[string]dschema.Attribute) {
	t.Helper()
	tags := modelTags(modelType)
	for name := range attrs {
		if !tags[name] {
			t.Errorf("[%s] schema attribute %q has no matching model field", level, name)
		}
	}
	for tag := range tags {
		if _, ok := attrs[tag]; !ok {
			keys := make([]string, 0, len(attrs))
			for k := range attrs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			t.Errorf("[%s] model tfsdk tag %q has no matching schema attribute (have %v)", level, tag, keys)
		}
	}
}

func TestK8sClusterDataSourceSchemaConsistency(t *testing.T) {
	var resp datasource.SchemaResponse
	NewK8sClusterDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema build errors: %v", resp.Diagnostics)
	}
	assertTagsMatch(t, "ds.cluster", reflect.TypeOf(K8sClusterDataSourceModel{}), resp.Schema.Attributes)

	kc, ok := resp.Schema.Attributes["kube_config"].(dschema.SingleNestedAttribute)
	if !ok {
		t.Fatal("kube_config is not a SingleNestedAttribute")
	}
	assertTagsMatch(t, "ds.cluster.kube_config", reflect.TypeOf(K8sKubeConfigModel{}), kc.Attributes)
	assertAttrTypesMatch(t, "ds.cluster.kube_config", kubeConfigAttrTypes(), kc.Attributes)
}

func TestK8sNodePoolDataSourceSchemaConsistency(t *testing.T) {
	var resp datasource.SchemaResponse
	NewK8sNodePoolDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema build errors: %v", resp.Diagnostics)
	}
	assertTagsMatch(t, "ds.node_pool", reflect.TypeOf(K8sNodePoolDataSourceModel{}), resp.Schema.Attributes)
}

func TestK8sFlavorsDataSourceSchemaConsistency(t *testing.T) {
	var resp datasource.SchemaResponse
	NewK8sFlavorsDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema build errors: %v", resp.Diagnostics)
	}
	assertTagsMatch(t, "ds.flavors", reflect.TypeOf(K8sFlavorsDataSourceModel{}), resp.Schema.Attributes)

	fl, ok := resp.Schema.Attributes["flavors"].(dschema.ListNestedAttribute)
	if !ok {
		t.Fatal("flavors is not a ListNestedAttribute")
	}
	assertTagsMatch(t, "ds.flavors.flavors", reflect.TypeOf(K8sFlavorSummary{}), fl.NestedObject.Attributes)
}

func TestK8sVersionsDataSourceSchemaConsistency(t *testing.T) {
	var resp datasource.SchemaResponse
	NewK8sVersionsDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema build errors: %v", resp.Diagnostics)
	}
	assertTagsMatch(t, "ds.versions", reflect.TypeOf(K8sVersionsDataSourceModel{}), resp.Schema.Attributes)

	vs, ok := resp.Schema.Attributes["versions"].(dschema.ListNestedAttribute)
	if !ok {
		t.Fatal("versions is not a ListNestedAttribute")
	}
	assertTagsMatch(t, "ds.versions.versions", reflect.TypeOf(K8sVersionSummary{}), vs.NestedObject.Attributes)
}
