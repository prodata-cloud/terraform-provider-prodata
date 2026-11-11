package client

import (
	"context"
	"fmt"
	"net/http"
)

func (c *Client) RequireClusterDeletionAPI(ctx context.Context, id int64, opts *RequestOpts) error {
	if id < 1 {
		return fmt.Errorf("invalid canonical cluster identity")
	}
	var caps struct {
		Contract              string `json:"contract"`
		CanonicalIDResume     bool   `json:"canonicalIdResume"`
		ReserveBeforeSubmit   bool   `json:"reserveBeforeSubmit"`
		RecoverableSubmission bool   `json:"recoverableSubmission"`
		UIDPinnedDelete       bool   `json:"uidPinnedDelete"`
	}
	path := fmt.Sprintf("/api/v2/kubernetes/clusters/%d/deletion/capabilities", id)
	if err := c.Do(ctx, http.MethodGet, path, nil, &caps, opts); err != nil {
		return err
	}
	if caps.Contract != "cluster-deletion-v1" || !caps.CanonicalIDResume || !caps.ReserveBeforeSubmit || !caps.RecoverableSubmission || !caps.UIDPinnedDelete {
		return fmt.Errorf("the panel must support durable UID-bound cluster deletion before a new teardown")
	}
	return nil
}

func (c *Cluster) KnownDeletion(expectedID int64) bool {
	return c != nil && c.ID == expectedID && expectedID > 0 && c.DeletionContract == "cluster-deletion-v1" && validateNodePoolRequestUUID(c.DeletionOperationUUID) == nil
}
