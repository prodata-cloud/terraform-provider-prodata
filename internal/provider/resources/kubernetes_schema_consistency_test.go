package resources

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// assertAttrTypesMatch checks that an attr.Type map (used to build an object value
// and to set it unknown in ModifyPlan) has exactly the keys of the nested schema.
func assertAttrTypesMatch(t *testing.T, level string, attrTypes map[string]attr.Type, attrs map[string]rschema.Attribute) {
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

// The plugin-framework only validates that a model's tfsdk tags line up with the
// schema at Get/Set time (i.e. during a real plan/apply), so a renamed or added
// attribute whose schema key and model tag drift apart compiles and unit-tests
// clean but panics in production. These tests close that gap for every k8s schema
// by asserting the tag set equals the attribute-key set at each nesting level.

func modelTags(modelType reflect.Type) map[string]bool {
	tags := map[string]bool{}
	for i := 0; i < modelType.NumField(); i++ {
		if tag, ok := modelType.Field(i).Tag.Lookup("tfsdk"); ok {
			tags[tag] = true
		}
	}
	return tags
}

func assertTagsMatch(t *testing.T, level string, modelType reflect.Type, attrs map[string]rschema.Attribute) {
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

func TestK8sClusterSchemaModelConsistency(t *testing.T) {
	var resp resource.SchemaResponse
	NewK8sClusterResource().(*K8sClusterResource).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema build errors: %v", resp.Diagnostics)
	}

	assertTagsMatch(t, "cluster", reflect.TypeOf(K8sClusterModel{}), resp.Schema.Attributes)

	kc, ok := resp.Schema.Attributes["kube_config"].(rschema.SingleNestedAttribute)
	if !ok {
		t.Fatal("kube_config is not a SingleNestedAttribute")
	}
	assertTagsMatch(t, "cluster.kube_config", reflect.TypeOf(K8sKubeConfigModel{}), kc.Attributes)
	assertAttrTypesMatch(t, "cluster.kube_config", kubeConfigAttrTypes(), kc.Attributes)

	dp, ok := resp.Schema.Attributes["default_node_pool"].(rschema.SingleNestedAttribute)
	if !ok {
		t.Fatal("default_node_pool is not a SingleNestedAttribute")
	}
	assertTagsMatch(t, "cluster.default_node_pool", reflect.TypeOf(K8sDefaultPoolModel{}), dp.Attributes)

	as, ok := dp.Attributes["autoscaling"].(rschema.SingleNestedAttribute)
	if !ok {
		t.Fatal("default_node_pool.autoscaling is not a SingleNestedAttribute")
	}
	assertTagsMatch(t, "cluster.default_node_pool.autoscaling", reflect.TypeOf(K8sAutoscalingModel{}), as.Attributes)
}

func TestK8sNodePoolSchemaModelConsistency(t *testing.T) {
	var resp resource.SchemaResponse
	NewK8sNodePoolResource().(*K8sNodePoolResource).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema build errors: %v", resp.Diagnostics)
	}

	assertTagsMatch(t, "node_pool", reflect.TypeOf(K8sNodePoolModel{}), resp.Schema.Attributes)

	as, ok := resp.Schema.Attributes["autoscaling"].(rschema.SingleNestedAttribute)
	if !ok {
		t.Fatal("autoscaling is not a SingleNestedAttribute")
	}
	assertTagsMatch(t, "node_pool.autoscaling", reflect.TypeOf(K8sAutoscalingModel{}), as.Attributes)
}
