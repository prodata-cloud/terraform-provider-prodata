package client

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
)

const ClusterCreationContract = "cluster-creation-v1"
const clusterCreationPath = "/api/v2/kubernetes/cluster-operations"

type ClusterCreationOperation struct {
	Contract    string `json:"contract"`
	RequestUUID string `json:"requestUuid"`
	ClusterID   *int64 `json:"clusterId"`
	Status      string `json:"status"`
	Exists      *bool  `json:"exists"`
	CanRelease  *bool  `json:"canRelease"`
	Ready       *bool  `json:"ready"`
	Epoch       int64  `json:"epoch"`
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name"`
}

func (v *ClusterCreationOperation) validate(requestUUID string) error {
	fp, err := hex.DecodeString(v.Fingerprint)
	if v.Contract != ClusterCreationContract || v.RequestUUID != requestUUID || v.Epoch < 1 || err != nil || len(fp) != 32 || v.Exists == nil || v.CanRelease == nil || v.Ready == nil || v.ClusterID != nil && *v.ClusterID < 1 {
		return fmt.Errorf("the API did not confirm the exact cluster creation identity; ownership was retained")
	}
	if v.Status == "NOT_SUBMITTED" {
		if *v.Exists || !*v.CanRelease || !*v.Ready {
			return fmt.Errorf("cluster cancellation lacks positive no-submission proof")
		}
		return nil
	}
	if v.ClusterID == nil || v.Name == "" || *v.CanRelease {
		return fmt.Errorf("cluster creation has no complete canonical ownership")
	}
	switch v.Status {
	case "RESERVED", "SUBMITTING", "UNVERIFIED", "RUNNING":
		if !*v.Exists || *v.Ready {
			return fmt.Errorf("cluster operation returned contradictory pending state")
		}
	case "SUCCESS":
		// Historical creation reports absence only after exact durable cluster teardown.
		if !*v.Ready {
			return fmt.Errorf("cluster creation has no positive completion proof")
		}
	default:
		return fmt.Errorf("unsupported durable cluster creation status")
	}
	return nil
}

func (c *Client) RequireClusterCreationAPI(ctx context.Context, opts *RequestOpts) error {
	var caps struct {
		Contract                string `json:"contract"`
		CallerUUID              bool   `json:"callerUuid"`
		ReserveBeforeSubmit     bool   `json:"reserveBeforeSubmit"`
		RecoverableSubmission   bool   `json:"recoverableSubmission"`
		ResolveTombstone        bool   `json:"resolveTombstone"`
		CompensatingDeleteFence bool   `json:"compensatingDeleteFence"`
		JournalIdentityFence    bool   `json:"journalIdentityFence"`
	}
	if err := c.Do(ctx, http.MethodGet, clusterCreationPath+"/capabilities", nil, &caps, opts); err != nil {
		return err
	}
	if caps.Contract != ClusterCreationContract || !caps.CallerUUID || !caps.ReserveBeforeSubmit || !caps.RecoverableSubmission || !caps.ResolveTombstone || !caps.CompensatingDeleteFence || !caps.JournalIdentityFence {
		return fmt.Errorf("the panel must support durable cluster creation, exact cancellation, durable journal identity, and fenced cleanup of partial creation before creating a cluster")
	}
	return nil
}

func (c *Client) ReserveClusterCreation(ctx context.Context, requestUUID string, request CreateClusterRequest, opts *RequestOpts) (*ClusterCreationOperation, error) {
	return c.clusterCreationCall(ctx, http.MethodPost, requestUUID, "/reservations", request, opts)
}
func (c *Client) SubmitClusterCreation(ctx context.Context, requestUUID string, opts *RequestOpts) (*ClusterCreationOperation, error) {
	return c.clusterCreationCall(ctx, http.MethodPost, requestUUID, "/submit", nil, opts)
}
func (c *Client) GetClusterCreation(ctx context.Context, requestUUID string, opts *RequestOpts) (*ClusterCreationOperation, error) {
	return c.clusterCreationCall(ctx, http.MethodGet, requestUUID, "", nil, opts)
}
func (c *Client) CancelClusterCreation(ctx context.Context, requestUUID string, opts *RequestOpts) (*ClusterCreationOperation, error) {
	return c.clusterCreationCall(ctx, http.MethodDelete, requestUUID, "", nil, opts)
}
func (c *Client) clusterCreationCall(ctx context.Context, method, requestUUID, suffix string, body any, opts *RequestOpts) (*ClusterCreationOperation, error) {
	if err := validateNodePoolRequestUUID(requestUUID); err != nil {
		return nil, fmt.Errorf("invalid durable cluster request UUID")
	}
	var out ClusterCreationOperation
	if err := c.Do(ctx, method, clusterCreationPath+"/"+requestUUID+suffix, body, &out, opts); err != nil {
		return nil, err
	}
	if err := out.validate(requestUUID); err != nil {
		return nil, err
	}
	return &out, nil
}
