package client

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const volumeIdentityVersion = "user-disk-v1"
const managedVolumesPath = "/api/v2/volumes/by-user-disk"

// ManagedVolume uses the persistent volume ID, rather than the VM attachment ID.
// An explicit exists=false in this versioned response is the only absence signal;
// an HTTP 404 may just mean that the server does not implement the stable API.
type ManagedVolume struct {
	CreationRequestUUID string `json:"creationRequestUuid"`
	Status              string `json:"status"`
	Volume
	IdentityVersion string `json:"identityVersion"`
	Exists          *bool  `json:"exists"`
	AttachmentID    *int64 `json:"attachmentId"`
}

func (v *ManagedVolume) validate(id int64) error {
	if v.CreationRequestUUID != "" {
		if err := validateVolumeRequestUUID(v.CreationRequestUUID); err != nil {
			return fmt.Errorf("the API returned an invalid creation UUID; existing identity was retained")
		}
	}
	switch v.Status {
	case "", "AVAILABLE", "RESERVED", "QUEUED", "RUNNING", "UNVERIFIED", "SUCCESS", "ERROR", "CREATING":
	default:
		return fmt.Errorf("the API returned an unsupported volume operation status; existing identity was retained")
	}

	if v.IdentityVersion != volumeIdentityVersion || v.ID <= 0 || v.ID != id {
		return fmt.Errorf("the API did not confirm the stable identity of volume %d; upgrade the panel API before managing volumes", id)
	}
	if v.Exists == nil {
		return fmt.Errorf("the existence of volume %d could not be verified; reconcile its platform record before retrying, existing state was preserved", id)
	}
	if *v.Exists && (v.Name == "" || v.Type == "" || v.Size <= 0 ||
		v.InUse != (v.AttachedID != nil) || v.InUse != (v.AttachmentID != nil)) {
		return fmt.Errorf("the API returned incomplete state for volume %d; keeping its existing identity", id)
	}
	if *v.Exists && v.InUse && (*v.AttachedID <= 0 || *v.AttachmentID <= 0) {
		return fmt.Errorf("the API returned invalid attachment identifiers for volume %d", id)
	}
	if !*v.Exists && (v.InUse || v.AttachedID != nil || v.AttachmentID != nil) {
		return fmt.Errorf("the API returned contradictory absence for volume %d; keeping its existing identity", id)
	}
	return nil
}

func (c *Client) CheckManagedVolumeAPI(ctx context.Context, opts *RequestOpts) error {
	var result struct {
		IdentityVersion string `json:"identityVersion"`
	}
	if err := c.Do(ctx, http.MethodGet, managedVolumesPath+"/capabilities", nil, &result, opts); err != nil {
		return err
	}
	if result.IdentityVersion != volumeIdentityVersion {
		return fmt.Errorf("the panel must support the stable volume API before creating volumes")
	}
	return nil
}

type ManagedVolumeInventory struct {
	Volumes             []Volume
	UnverifiedVolumeIDs []int64
}

func (c *Client) GetManagedVolume(ctx context.Context, id int64, opts *RequestOpts) (*ManagedVolume, error) {
	var volume ManagedVolume
	if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("%s/%d", managedVolumesPath, id), nil, &volume, opts); err != nil {
		return nil, fmt.Errorf("cannot verify the stable identity of volume %d (the panel must support the stable volume API): %w", id, err)
	}
	if err := volume.validate(id); err != nil {
		return nil, err
	}
	return &volume, nil
}

func (c *Client) GetManagedVolumes(ctx context.Context, opts *RequestOpts) ([]Volume, error) {
	result, err := c.GetManagedVolumeInventory(ctx, opts)
	if err != nil {
		return nil, err
	}
	if len(result.UnverifiedVolumeIDs) > 0 {
		return nil, fmt.Errorf("some volume identities could not be verified: %v", result.UnverifiedVolumeIDs)
	}
	return result.Volumes, nil
}

func (c *Client) GetManagedVolumeInventory(ctx context.Context, opts *RequestOpts) (*ManagedVolumeInventory, error) {
	var result struct {
		IdentityVersion     string          `json:"identityVersion"`
		Volumes             []ManagedVolume `json:"volumes"`
		UnverifiedVolumeIDs []int64         `json:"unverifiedVolumeIds"`
	}
	if err := c.Do(ctx, http.MethodGet, managedVolumesPath, nil, &result, opts); err != nil {
		return nil, err
	}
	if result.IdentityVersion != volumeIdentityVersion || result.Volumes == nil {
		return nil, fmt.Errorf("the API did not confirm stable volume identities; upgrade the panel API before listing volumes")
	}
	volumes := make([]Volume, 0, len(result.Volumes))
	seen := make(map[int64]bool, len(result.Volumes))
	for _, volume := range result.Volumes {
		if err := volume.validate(volume.ID); err != nil {
			return nil, err
		}
		if !*volume.Exists || seen[volume.ID] {
			return nil, fmt.Errorf("the API returned an inconsistent stable volume list")
		}
		seen[volume.ID] = true
		volumes = append(volumes, volume.Volume)
	}
	for _, id := range result.UnverifiedVolumeIDs {
		if id <= 0 || seen[id] {
			return nil, fmt.Errorf("the API returned contradictory unverified volume identities")
		}
		seen[id] = true
	}
	return &ManagedVolumeInventory{Volumes: volumes, UnverifiedVolumeIDs: result.UnverifiedVolumeIDs}, nil
}

func (c *Client) UpdateManagedVolume(ctx context.Context, id int64, req UpdateVolumeRequest) (*Volume, error) {
	var volume ManagedVolume
	opts := &RequestOpts{Region: req.Region, ProjectTag: req.ProjectTag}
	if err := c.Do(ctx, http.MethodPut, fmt.Sprintf("%s/%d", managedVolumesPath, id), req, &volume, opts); err != nil {
		return nil, err
	}
	if err := volume.validate(id); err != nil {
		return nil, err
	}
	if !*volume.Exists {
		return nil, fmt.Errorf("volume %d disappeared during update", id)
	}
	return &volume.Volume, nil
}

func (c *Client) DeleteManagedVolume(ctx context.Context, id int64, opts *RequestOpts) error {
	return c.DeleteManagedVolumeWithIdentity(ctx, id, "", opts)
}
func (c *Client) DeleteManagedVolumeWithIdentity(ctx context.Context, id int64, expected string, opts *RequestOpts) error {
	endpoint := fmt.Sprintf("%s/%d", managedVolumesPath, id)
	if expected != "" {
		if expected != strings.TrimSpace(expected) || len(expected) > 2048 || !strings.Contains(expected, ":") {
			return fmt.Errorf("a complete physical source volume ID is required")
		}
		endpoint += "?expectedSourceVolid=" + url.QueryEscape(expected)
	}
	var volume ManagedVolume
	if err := c.Do(ctx, http.MethodDelete, endpoint, nil, &volume, opts); err != nil {
		return err
	}
	if err := volume.validate(id); err != nil {
		return err
	}
	if *volume.Exists {
		return fmt.Errorf("the API has not confirmed deletion of volume %d", id)
	}
	return nil
}
