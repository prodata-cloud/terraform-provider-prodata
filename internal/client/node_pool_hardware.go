package client

import (
	"context"
	"fmt"
	"net/http"
)

type NodePoolHardwareRequest struct {
	VCPU     int64 `json:"vcpu"`
	RAM      int64 `json:"ram"`
	DiskSize int64 `json:"diskSize"`
}

type NodePoolHardware struct {
	ProtocolVersion int   `json:"protocolVersion"`
	VCPU            int64 `json:"vcpu"`
	RAM             int64 `json:"ram"`
	DiskSize        int64 `json:"diskSize"`
	Ready           *bool `json:"ready"`
}

type ControlPlaneHardware struct {
	NodePoolHardware
	MasterFlavorID int64 `json:"masterFlavorId"`
}

func (c *Client) ControlPlaneHardware(ctx context.Context, id int64, opts *RequestOpts) (*ControlPlaneHardware, error) {
	return c.controlPlaneHardware(ctx, http.MethodGet, id, nil, opts)
}
func (c *Client) ResizeControlPlaneHardware(ctx context.Context, id, flavorID int64, opts *RequestOpts) (*ControlPlaneHardware, error) {
	return c.controlPlaneHardware(ctx, http.MethodPut, id, map[string]int64{"masterFlavorId": flavorID}, opts)
}
func (c *Client) controlPlaneHardware(ctx context.Context, method string, id int64, body any, opts *RequestOpts) (*ControlPlaneHardware, error) {
	var result ControlPlaneHardware
	if err := c.Do(ctx, method, fmt.Sprintf("/api/v2/kubernetes/clusters/%d/hardware", id), body, &result, opts); err != nil {
		return nil, fmt.Errorf("control-plane hardware requires a compatible panel and Kubernetes service: %w", err)
	}
	if result.ProtocolVersion != 1 || result.MasterFlavorID < 1 || result.VCPU < 1 || result.RAM < 1 || result.DiskSize < 1 || result.Ready == nil {
		return nil, fmt.Errorf("control-plane hardware acknowledgement is unsupported or incomplete")
	}
	return &result, nil
}

func (c *Client) NodePoolHardware(ctx context.Context, id int64, opts *RequestOpts) (*NodePoolHardware, error) {
	return c.nodePoolHardware(ctx, http.MethodGet, id, nil, opts)
}

func (c *Client) ResizeNodePoolHardware(ctx context.Context, id int64, order NodePoolHardwareRequest, opts *RequestOpts) (*NodePoolHardware, error) {
	return c.nodePoolHardware(ctx, http.MethodPut, id, order, opts)
}

func (c *Client) nodePoolHardware(ctx context.Context, method string, id int64, body any, opts *RequestOpts) (*NodePoolHardware, error) {
	var result NodePoolHardware
	if err := c.Do(ctx, method, fmt.Sprintf("/api/v2/kubernetes/node-pools/%d/hardware", id), body, &result, opts); err != nil {
		return nil, fmt.Errorf("worker hardware API requires a compatible panel and Kubernetes service: %w", err)
	}
	if result.ProtocolVersion != 1 || result.VCPU < 1 || result.RAM < 1 || result.DiskSize < 1 || result.Ready == nil {
		return nil, fmt.Errorf("worker hardware API returned an unsupported or incomplete acknowledgement")
	}
	return &result, nil
}
