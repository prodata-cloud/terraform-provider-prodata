package client

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	uuid "github.com/hashicorp/go-uuid"
)

const VolumeCreationContract = "volume-creation-v1"
const volumeCreationPath = managedVolumesPath + "/creation"

type VolumeCreation struct {
	Contract     string `json:"contract"`
	RequestUUID  string `json:"requestUuid"`
	VolumeID     *int64 `json:"volumeId"`
	Region       string `json:"region"`
	ProjectTag   string `json:"projectTag"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Size         int64  `json:"size"`
	Status       string `json:"status"`
	Exists       *bool  `json:"exists"`
	CanRelease   *bool  `json:"canRelease"`
	ErrorMessage string `json:"errorMessage"`
}

func validateVolumeRequestUUID(requestUUID string) error {
	if _, err := uuid.ParseUUID(requestUUID); err != nil || strings.ToLower(requestUUID) != requestUUID {
		return fmt.Errorf("invalid durable volume request UUID")
	}
	return nil
}

func (v *VolumeCreation) validate(requestUUID string, opts *RequestOpts) error {
	if v.Contract != VolumeCreationContract || v.RequestUUID != requestUUID || v.CanRelease == nil ||
		v.Region == "" || v.ProjectTag == "" || v.VolumeID != nil && *v.VolumeID <= 0 {
		return fmt.Errorf("the API did not confirm the durable volume creation identity; existing ownership was retained")
	}
	if opts != nil && (opts.Region != "" && opts.Region != v.Region || opts.ProjectTag != "" && opts.ProjectTag != v.ProjectTag) {
		return fmt.Errorf("the API returned a volume creation in another scope; existing ownership was retained")
	}
	switch v.Status {
	case "UNKNOWN":
		if v.Exists != nil || *v.CanRelease || v.VolumeID != nil {
			return fmt.Errorf("unknown volume creation returned a contradictory release proof")
		}
	case "CANCELLED":
		if v.VolumeID != nil && (v.Name == "" || v.Type == "" || v.Size <= 0) {
			return fmt.Errorf("cancelled canonical volume returned incomplete immutable ownership")
		}
		if v.Exists == nil || *v.Exists || !*v.CanRelease {
			return fmt.Errorf("volume cancellation has no positive release proof")
		}
	case "RESERVED", "SUBMITTING", "UNVERIFIED", "READY":
		if v.Exists == nil || !*v.Exists || *v.CanRelease || v.VolumeID == nil || v.Name == "" || v.Type == "" || v.Size <= 0 {
			return fmt.Errorf("volume creation returned incomplete ownership; the request UUID was retained")
		}
	default:
		return fmt.Errorf("unsupported durable volume creation state; the request UUID was retained")
	}
	return nil
}

func (c *Client) CheckVolumeCreationAPI(ctx context.Context, opts *RequestOpts) error {
	var result struct {
		Contract            string `json:"contract"`
		ReserveBeforeSubmit bool   `json:"reserveBeforeSubmit"`
		RequestLookup       bool   `json:"requestLookup"`
		CancelTombstone     bool   `json:"cancelTombstone"`
		CanonicalIdentity   string `json:"canonicalIdentity"`
	}
	if err := c.Do(ctx, http.MethodGet, volumeCreationPath+"/capabilities", nil, &result, opts); err != nil {
		return err
	}
	if result.Contract != VolumeCreationContract || !result.ReserveBeforeSubmit || !result.RequestLookup || !result.CancelTombstone || result.CanonicalIdentity != volumeIdentityVersion {
		return fmt.Errorf("the panel must support durable volume creation before storage can be allocated")
	}
	return nil
}

func (c *Client) ReserveVolumeCreation(ctx context.Context, requestUUID string, req CreateVolumeRequest) (*VolumeCreation, error) {
	result, err := c.volumeCreation(ctx, http.MethodPost, requestUUID, "", req, &RequestOpts{Region: req.Region, ProjectTag: req.ProjectTag})
	if err != nil {
		return nil, err
	}
	if result.Status != "CANCELLED" && (result.Name != req.Name || result.Type != req.Type || result.Size != req.Size) {
		return nil, fmt.Errorf("the API returned another immutable volume creation specification; ownership was retained")
	}
	return result, nil
}
func (c *Client) SubmitVolumeCreation(ctx context.Context, requestUUID string, opts *RequestOpts) (*VolumeCreation, error) {
	return c.volumeCreation(ctx, http.MethodPost, requestUUID, "/submit", nil, opts)
}
func (c *Client) GetVolumeCreation(ctx context.Context, requestUUID string, opts *RequestOpts) (*VolumeCreation, error) {
	return c.volumeCreation(ctx, http.MethodGet, requestUUID, "", nil, opts)
}
func (c *Client) CancelVolumeCreation(ctx context.Context, requestUUID string, opts *RequestOpts) (*VolumeCreation, error) {
	return c.volumeCreation(ctx, http.MethodDelete, requestUUID, "", nil, opts)
}
func (c *Client) volumeCreation(ctx context.Context, method, requestUUID, suffix string, body any, opts *RequestOpts) (*VolumeCreation, error) {
	if err := validateVolumeRequestUUID(requestUUID); err != nil {
		return nil, err
	}
	var result VolumeCreation
	if err := c.Do(ctx, method, volumeCreationPath+"/"+requestUUID+suffix, body, &result, opts); err != nil {
		return nil, err
	}
	if err := result.validate(requestUUID, opts); err != nil {
		return nil, err
	}
	return &result, nil
}
