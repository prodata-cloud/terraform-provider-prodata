package resources

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Known-answer vector: sha256 of the 14-byte string "#cloud-config\n".
// Computed with: printf '#cloud-config\n' | shasum -a 256
const knownCloudConfigHash = "88c95955b024402aa9572b663f7eeb134f01343bb92af27b50e97e72b22c565f"

func TestUserDataHashHex_knownVector(t *testing.T) {
	if got := userDataHashHex("#cloud-config\n"); got != knownCloudConfigHash {
		t.Errorf("userDataHashHex = %q, want %q (must match HCL sha256() for migration parity)", got, knownCloudConfigHash)
	}
}

func TestClassifyUserData(t *testing.T) {
	cases := []struct {
		name string
		in   types.String
		want userDataState
	}{
		{"present", types.StringValue("#cloud-config\n"), userDataPresent},
		{"null", types.StringNull(), userDataNull},
		{"unknown", types.StringUnknown(), userDataUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyUserData(c.in); got != c.want {
				t.Errorf("classifyUserData(%s) = %v, want %v", c.name, got, c.want)
			}
		})
	}
}

func TestUserDataReplaceNeeded(t *testing.T) {
	const H = knownCloudConfigHash
	cases := []struct {
		name     string
		stored   string
		storedOK bool
		st       userDataState
		hexNow   string
		want     bool
	}{
		{"no baseline (create/import/upgrade) -> adopt", "", false, userDataPresent, H, false},
		{"baseline, present, same -> no replace", H, true, userDataPresent, H, false},
		{"baseline, present, different -> replace", H, true, userDataPresent, "ff" + H[2:], true},
		{"baseline, removed (null) -> no replace, retain", H, true, userDataNull, "", false},
		{"baseline, unknown -> no replace, retain", H, true, userDataUnknown, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := userDataReplaceNeeded(c.stored, c.storedOK, c.st, c.hexNow); got != c.want {
				t.Errorf("userDataReplaceNeeded(%q,%v,%v,%q) = %v, want %v",
					c.stored, c.storedOK, c.st, c.hexNow, got, c.want)
			}
		})
	}
}

func TestMarshalUnmarshalUserDataHash_roundTrip(t *testing.T) {
	blob, err := marshalUserDataHash(knownCloudConfigHash)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Must be valid JSON + UTF-8 (the SetKey contract): a JSON string literal.
	if string(blob) != `"`+knownCloudConfigHash+`"` {
		t.Errorf("blob = %s, want a JSON string of the hex", blob)
	}
	got, ok := unmarshalUserDataHash(blob)
	if !ok || got != knownCloudConfigHash {
		t.Errorf("round-trip = (%q,%v), want (%q,true)", got, ok, knownCloudConfigHash)
	}
}

func TestUnmarshalUserDataHash_corruptOrEmptyIsAbsent(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"nil (absent key)", nil},
		{"empty", []byte{}},
		{"empty JSON string", []byte(`""`)},
		{"malformed JSON", []byte(`{not json`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, ok := unmarshalUserDataHash(c.in); ok || got != "" {
				t.Errorf("unmarshalUserDataHash(%q) = (%q,%v), want (\"\",false) so a corrupt blob can't force spurious replace", c.in, got, ok)
			}
		})
	}
}
