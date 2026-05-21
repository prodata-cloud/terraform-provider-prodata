// Package tfutil holds small helpers shared across the provider's resource and
// data source packages (which cannot share unexported helpers directly).
package tfutil

import "github.com/hashicorp/terraform-plugin-framework/types"

// StringOrNull maps an empty server string to a null types.String and any other
// value to a known one. It centralizes the "empty means null in state" idiom
// that otherwise repeats as a four-line if/else at every computed-string field.
func StringOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}
