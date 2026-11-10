package client

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"

	uuid "github.com/hashicorp/go-uuid"
)

const NodePoolLifecycleContract = "pool-lifecycle-v1"
const NodePoolCreateOperation = "CREATE"
const NodePoolDeleteOperation = "DELETE"

type NodePoolOperation struct {
	Contract     string `json:"contract"`
	RequestUUID  string `json:"requestUuid"`
	ClusterID    int64  `json:"clusterId"`
	PoolID       *int64 `json:"poolId"`
	Operation    string `json:"operation"`
	Status       string `json:"status"`
	Exists       *bool  `json:"exists"`
	CanRelease   *bool  `json:"canRelease"`
	Ready        *bool  `json:"ready"`
	NodePoolName string `json:"nodePoolName"`
	Epoch        int64  `json:"epoch"`
	Fingerprint  string `json:"fingerprint"`
}

type NodePoolOperationRequest struct {
	Operation string                 `json:"operation"`
	PoolID    *int64                 `json:"poolId,omitempty"`
	Create    *CreateNodePoolRequest `json:"create,omitempty"`
}

func nodePoolOperationPath(clusterID int64) string {
	return fmt.Sprintf("/api/v2/kubernetes/clusters/%d/node-pool-operations", clusterID)
}

func validateNodePoolRequestUUID(value string) error {
	parsed, err := uuid.ParseUUID(value)
	if err != nil || len(parsed) != 16 || fmt.Sprintf("%x-%x-%x-%x-%x", parsed[0:4], parsed[4:6], parsed[6:8], parsed[8:10], parsed[10:16]) != value {
		return fmt.Errorf("invalid durable node pool request UUID")
	}
	return nil
}

func (v *NodePoolOperation) validate(clusterID int64, requestUUID, operation string) error {
	fp, fpErr := hex.DecodeString(v.Fingerprint)
	if clusterID < 1 || v.Contract != NodePoolLifecycleContract || v.RequestUUID != requestUUID || v.ClusterID != clusterID ||
		v.Operation != operation || (operation != NodePoolCreateOperation && operation != NodePoolDeleteOperation) ||
		v.Epoch < 1 || fpErr != nil || len(fp) != 32 || v.Exists == nil || v.CanRelease == nil || v.Ready == nil || v.PoolID != nil && *v.PoolID < 1 {
		return fmt.Errorf("the API did not confirm the exact node pool operation identity; ownership was retained")
	}
	if v.Status == "NOT_SUBMITTED" {
		if !*v.CanRelease || !*v.Ready || *v.Exists != (operation == NodePoolDeleteOperation) || operation == NodePoolDeleteOperation && v.PoolID == nil {
			return fmt.Errorf("node pool cancellation lacks positive identity-bound proof; ownership was retained")
		}
		return nil
	}
	if *v.CanRelease || v.PoolID == nil || v.NodePoolName == "" && (operation != NodePoolDeleteOperation || v.Status != "SUCCESS") {
		return fmt.Errorf("the API returned incomplete canonical node pool ownership")
	}
	switch v.Status {
	case "RESERVED", "SUBMITTING", "UNVERIFIED", "RUNNING":
		if !*v.Exists || *v.Ready {
			return fmt.Errorf("node pool operation returned contradictory pending state")
		}
	case "SUCCESS":
		if !*v.Ready || operation == NodePoolDeleteOperation && *v.Exists {
			return fmt.Errorf("node pool operation returned no positive completion proof")
		}
	default:
		return fmt.Errorf("unsupported durable node pool operation status; ownership was retained")
	}
	return nil
}

func (c *Client) CheckNodePoolLifecycleAPI(ctx context.Context, clusterID int64, opts *RequestOpts) error {
	if clusterID < 1 {
		return fmt.Errorf("invalid cluster identity")
	}
	var result struct {
		Contract              string `json:"contract"`
		CallerUUID            bool   `json:"callerUuid"`
		ReserveBeforeSubmit   bool   `json:"reserveBeforeSubmit"`
		RecoverableSubmission bool   `json:"recoverableSubmission"`
		UIDPinnedDelete       bool   `json:"uidPinnedDelete"`
	}
	if err := c.Do(ctx, http.MethodGet, nodePoolOperationPath(clusterID)+"/capabilities", nil, &result, opts); err != nil {
		return err
	}
	if result.Contract != NodePoolLifecycleContract || !result.CallerUUID || !result.ReserveBeforeSubmit || !result.RecoverableSubmission || !result.UIDPinnedDelete {
		return fmt.Errorf("the panel must support durable, UID-bound node pool creation and deletion before changing pools")
	}
	return nil
}

func (c *Client) ReserveNodePoolOperation(ctx context.Context, clusterID int64, requestUUID string, req NodePoolOperationRequest, opts *RequestOpts) (*NodePoolOperation, error) {
	if req.Operation == NodePoolCreateOperation {
		if req.Create == nil || req.Create.ClusterID != clusterID || req.PoolID != nil {
			return nil, fmt.Errorf("invalid canonical node pool creation request")
		}
	} else if req.Operation != NodePoolDeleteOperation || req.PoolID == nil || *req.PoolID < 1 || req.Create != nil {
		return nil, fmt.Errorf("invalid canonical node pool deletion request")
	}
	result, err := c.nodePoolOperation(ctx, http.MethodPost, clusterID, requestUUID, req.Operation, "/reservations", req, opts)
	if err != nil {
		return nil, err
	}
	if req.PoolID != nil && (result.PoolID == nil || *result.PoolID != *req.PoolID) {
		return nil, fmt.Errorf("the API returned another canonical pool identity")
	}
	if req.Create != nil && result.Status != "NOT_SUBMITTED" && result.NodePoolName != req.Create.NodePoolName {
		return nil, fmt.Errorf("the API returned another node pool specification")
	}
	return result, nil
}
func (c *Client) SubmitNodePoolOperation(ctx context.Context, clusterID int64, requestUUID, operation string, opts *RequestOpts) (*NodePoolOperation, error) {
	return c.nodePoolOperation(ctx, http.MethodPost, clusterID, requestUUID, operation, "/submit", nil, opts)
}
func (c *Client) GetNodePoolOperation(ctx context.Context, clusterID int64, requestUUID, operation string, opts *RequestOpts) (*NodePoolOperation, error) {
	return c.nodePoolOperation(ctx, http.MethodGet, clusterID, requestUUID, operation, "", nil, opts)
}
func (c *Client) CancelNodePoolCreation(ctx context.Context, clusterID int64, requestUUID string, opts *RequestOpts) (*NodePoolOperation, error) {
	return c.nodePoolOperation(ctx, http.MethodDelete, clusterID, requestUUID, NodePoolCreateOperation, "", nil, opts)
}
func (c *Client) nodePoolOperation(ctx context.Context, method string, clusterID int64, requestUUID, operation, suffix string, body any, opts *RequestOpts) (*NodePoolOperation, error) {
	if clusterID < 1 {
		return nil, fmt.Errorf("invalid cluster identity")
	}
	if err := validateNodePoolRequestUUID(requestUUID); err != nil {
		return nil, err
	}
	var result NodePoolOperation
	if err := c.Do(ctx, method, nodePoolOperationPath(clusterID)+"/"+requestUUID+suffix, body, &result, opts); err != nil {
		return nil, err
	}
	if err := result.validate(clusterID, requestUUID, operation); err != nil {
		return nil, err
	}
	return &result, nil
}
