package resources

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// userDataHashPrivateKey is the resource private-state key holding the sha256 (hex) of the
// last applied write-only user_data payload. Used to detect payload changes across plans.
// Must not start with '.' (reserved by the framework).
const userDataHashPrivateKey = "user_data_sha256"

// userDataState classifies the config value of the write-only user_data attribute. null and
// unknown are deliberately distinct: a removed (null) value must not force replacement or
// clear the baseline (cloud-init already ran once and can't be un-run), and an unknown value
// (an unresolved interpolation such as templatefile referencing another resource) must defer
// the decision rather than be mistaken for a removal.
type userDataState int

const (
	userDataNull    userDataState = iota // config value is null (unset / removed)
	userDataUnknown                      // config value is unknown (not yet resolved at plan time)
	userDataPresent                      // config value is known and non-null
)

func classifyUserData(v types.String) userDataState {
	switch {
	case v.IsUnknown():
		return userDataUnknown
	case v.IsNull():
		return userDataNull
	default:
		return userDataPresent
	}
}

// userDataHashHex returns the lowercase hex sha256 of a present user_data value. It matches
// Terraform's built-in sha256() so a migrated config diffs cleanly.
func userDataHashHex(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// userDataReplaceNeeded reports whether a user_data change requires VM replacement. It is
// true ONLY when a baseline exists (storedOK) AND the config carries a known, non-null value
// whose hash differs. A missing baseline (create / post-import / post-upgrade) adopts; a
// null or unknown config value is never a change here (and never clears the baseline).
func userDataReplaceNeeded(stored string, storedOK bool, st userDataState, hexNow string) bool {
	if !storedOK || st != userDataPresent {
		return false
	}
	return stored != hexNow
}

// marshalUserDataHash encodes a hex digest as the JSON []byte blob SetKey requires.
func marshalUserDataHash(hexDigest string) ([]byte, error) {
	return json.Marshal(hexDigest)
}

// unmarshalUserDataHash decodes a private-state blob into the stored hex digest. Absent
// (nil), empty, an empty JSON string, or malformed input all return ("", false) — treated as
// "no baseline" so a hand-edited/corrupt blob can never become "" and force a spurious
// replace on every plan.
func unmarshalUserDataHash(b []byte) (hexDigest string, ok bool) {
	if len(b) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil || s == "" {
		return "", false
	}
	return s, true
}
