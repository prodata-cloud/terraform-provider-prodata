package datasources

import "terraform-provider-prodata/internal/client"

// Mirrors resources.versioningFromConfig — kept package-local on purpose so the
// data source has no dependency on the resources package. Keep the two in sync
// when the canonical TF wire form changes.
func versioningFromConfig(vc *client.VersioningConfiguration) string {
	if vc == nil {
		return "disabled"
	}
	switch vc.Status {
	case "ENABLED":
		return "enabled"
	case "SUSPENDED":
		return "suspended"
	default:
		return "disabled"
	}
}

// Mirrors resources.objectLockFromConfig — see comment on versioningFromConfig.
func objectLockFromConfig(olc *client.ObjectLockConfiguration) bool {
	return olc != nil && olc.ObjectLockEnabled == "ENABLED"
}
