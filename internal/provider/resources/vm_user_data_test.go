package resources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"terraform-provider-prodata/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func vmSchema(t *testing.T) schema.Schema {
	t.Helper()
	var resp resource.SchemaResponse
	NewVmResource().Schema(context.Background(), resource.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema returned diagnostics: %v", resp.Diagnostics)
	}
	return resp.Schema
}

// TestVm_UserDataSchema locks the write-only design: user_data must be Optional + WriteOnly
// + not Computed so the raw payload never lands in state or a plan. The user_data_hash
// attribute must be ABSENT — change detection moved to provider-computed sha256 in private
// state. A timeouts block must exist because the cloud-init wait can exceed the old 5m default.
func TestVm_UserDataSchema(t *testing.T) {
	s := vmSchema(t)

	ud, ok := s.Attributes["user_data"].(schema.StringAttribute)
	if !ok {
		t.Fatal("prodata_vm must expose a string `user_data` attribute")
	}
	if !ud.IsOptional() {
		t.Error("user_data must be Optional")
	}
	if !ud.IsWriteOnly() {
		t.Error("user_data must be WriteOnly (raw payload never stored in state nor shown in a plan)")
	}
	if ud.IsComputed() {
		t.Error("user_data must not be Computed")
	}

	// user_data carries both guards: the byte-length cap and the prefix validator.
	// Lock that the prefix validator is wired (it is the leak-safe replacement for the
	// old regex matcher), and that at least the two expected validators are present.
	udValidators := ud.StringValidators()
	if len(udValidators) < 2 {
		t.Errorf("user_data must carry at least 2 validators (length cap + prefix), got %d", len(udValidators))
	}
	foundPrefix := false
	for _, v := range udValidators {
		if _, ok := v.(userDataPrefixValidator); ok {
			foundPrefix = true
			break
		}
	}
	if !foundPrefix {
		t.Error("user_data must carry the userDataPrefixValidator (leak-safe prefix check)")
	}

	if _, ok := s.Attributes["user_data_hash"]; ok {
		t.Error("user_data_hash must be removed — the provider now computes the hash internally (private state)")
	}

	if _, ok := s.Attributes["timeouts"]; !ok {
		t.Error("prodata_vm must expose a `timeouts` block (cloud-init wait can exceed the old 5m default)")
	}
}

// runUserDataValidators runs every validator wired on the user_data attribute against a
// value, mirroring exactly what Terraform does at plan time.
func runUserDataValidators(t *testing.T, value string) validator.StringResponse {
	t.Helper()
	ud, ok := vmSchema(t).Attributes["user_data"].(schema.StringAttribute)
	if !ok {
		t.Fatal("user_data attribute missing")
	}
	var resp validator.StringResponse
	req := validator.StringRequest{
		Path:        path.Root("user_data"),
		ConfigValue: types.StringValue(value),
	}
	for _, v := range ud.StringValidators() {
		v.ValidateString(context.Background(), req, &resp)
	}
	return resp
}

func TestVm_UserDataValidators(t *testing.T) {
	const header = "#cloud-config\n" // 14 bytes

	// Exactly 65536 bytes (the backend cap; LengthAtMost uses len() == bytes).
	exactlyMax := header + strings.Repeat("a", 65536-len(header))
	// 65537 bytes — one over.
	overMax := header + strings.Repeat("a", 65536-len(header)+1)
	// Multibyte: ~66k bytes but only ~33k runes — proves the limit is BYTES, not runes
	// (a rune-based validator would wrongly accept this; the backend would then reject it).
	multibyteOver := header + strings.Repeat("é", 33000) // "é" == 2 bytes

	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{"cloud-config", "#cloud-config\npackages: [htop]\n", false},
		{"shebang", "#!/bin/bash\necho hi\n", false},
		{"exactly 64KiB", exactlyMax, false},
		{"one byte over 64KiB", overMax, true},
		{"multibyte over byte limit", multibyteOver, true},
		{"bad prefix (bare yaml)", "packages: [htop]\n", true},
		{"empty", "", true},
		{"leading space before header", " #cloud-config\n", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := runUserDataValidators(t, tc.value)
			gotErr := resp.Diagnostics.HasError()
			if gotErr != tc.wantErr {
				t.Errorf("len=%d bytes: gotErr=%v wantErr=%v; diags=%v",
					len(tc.value), gotErr, tc.wantErr, resp.Diagnostics)
			}
		})
	}
}

// TestVm_UserDataPrefixValidatorNoLeak guards against the old regex-based validator's
// behavior of echoing the rejected value verbatim into the diagnostic. The prefix
// validator must report an error WITHOUT including the raw payload, so cloud-init
// contents (which routinely carry secrets) never reach plan/validate output or logs.
func TestVm_UserDataPrefixValidatorNoLeak(t *testing.T) {
	const sentinel = "PLAINTEXT_SECRET_SENTINEL_xyz"
	// Bad prefix (no "#cloud-config"/"#!") so it is rejected, with the secret embedded.
	bad := "password: " + sentinel + "\n"

	resp := runUserDataValidators(t, bad)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a diagnostic error for a bad-prefix user_data value")
	}

	var joined strings.Builder
	for _, d := range resp.Diagnostics {
		joined.WriteString(d.Summary())
		joined.WriteString("\n")
		joined.WriteString(d.Detail())
		joined.WriteString("\n")
	}
	if strings.Contains(joined.String(), sentinel) {
		t.Errorf("user_data validator leaked the raw payload into the diagnostic: %q", joined.String())
	}
}

// TestCreateVmRequest_marshalsUserData guards the wire contract: the backend reads the
// field as camelCase "userData", it is sent PLAIN (not base64), and it must be omitted
// entirely when unset so existing no-user_data creates are byte-for-byte unchanged.
func TestCreateVmRequest_marshalsUserData(t *testing.T) {
	ud := "#cloud-config\n"
	b, err := json.Marshal(client.CreateVmRequest{Name: "x", UserData: &ud})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"userData":"#cloud-config\n"`) {
		t.Errorf("expected camelCase \"userData\" in JSON, got: %s", b)
	}

	// Plain, not base64: the backend expects the raw payload and does the NoCloud
	// encoding itself. A base64'd payload would be double-encoded and break cloud-init.
	if encoded := base64.StdEncoding.EncodeToString([]byte(ud)); strings.Contains(string(b), encoded) {
		t.Errorf("userData must be sent PLAIN, but JSON contained the base64 encoding %q: %s", encoded, b)
	}

	b2, err := json.Marshal(client.CreateVmRequest{Name: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b2), "userData") {
		t.Errorf("userData must be omitted when nil, got: %s", b2)
	}
}
