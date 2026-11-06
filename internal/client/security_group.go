package client

import (
	"context"
	"fmt"
	"net/http"
)

type SecurityGroupRule struct {
	Direction       string `json:"direction"`
	Action          string `json:"action"`
	Protocol        string `json:"protocol"`
	Source          string `json:"source"`
	Destination     string `json:"destination"`
	SourcePort      string `json:"sourcePort"`
	DestinationPort string `json:"destinationPort"`
}

type SecurityGroup struct {
	ID    int64               `json:"id"`
	Name  string              `json:"name"`
	Rules []SecurityGroupRule `json:"rules"`
}

type SecurityGroupRequest struct {
	Name  string              `json:"name"`
	Rules []SecurityGroupRule `json:"rules"`
}

type SecurityGroupAttachment struct {
	GroupID     int64 `json:"groupId"`
	VmID        int64 `json:"vmId"`
	InterfaceID int64 `json:"interfaceId"`
}

func (c *Client) GetSecurityGroup(ctx context.Context, id int64, opts *RequestOpts) (*SecurityGroup, error) {
	var result SecurityGroup
	if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v2/security-groups/%d", id), nil, &result, opts); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) CreateSecurityGroup(ctx context.Context, body SecurityGroupRequest, opts *RequestOpts) (*SecurityGroup, error) {
	var result SecurityGroup
	if err := c.Do(ctx, http.MethodPost, "/api/v2/security-groups", body, &result, opts); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) UpdateSecurityGroup(ctx context.Context, id int64, body SecurityGroupRequest, opts *RequestOpts) (*SecurityGroup, error) {
	var result SecurityGroup
	if err := c.Do(ctx, http.MethodPut, fmt.Sprintf("/api/v2/security-groups/%d", id), body, &result, opts); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) DeleteSecurityGroup(ctx context.Context, id int64, opts *RequestOpts) error {
	return c.Do(ctx, http.MethodDelete, fmt.Sprintf("/api/v2/security-groups/%d", id), nil, nil, opts)
}

func securityGroupAttachmentPath(groupID, vmID, interfaceID int64) string {
	return fmt.Sprintf("/api/v2/security-groups/%d/attachments/%d/%d", groupID, vmID, interfaceID)
}

func (c *Client) GetSecurityGroupAttachment(ctx context.Context, groupID, vmID, interfaceID int64, opts *RequestOpts) (*SecurityGroupAttachment, error) {
	var result SecurityGroupAttachment
	if err := c.Do(ctx, http.MethodGet, securityGroupAttachmentPath(groupID, vmID, interfaceID), nil, &result, opts); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) AttachSecurityGroup(ctx context.Context, groupID, vmID, interfaceID int64, opts *RequestOpts) (*SecurityGroupAttachment, error) {
	var result SecurityGroupAttachment
	if err := c.Do(ctx, http.MethodPut, securityGroupAttachmentPath(groupID, vmID, interfaceID), nil, &result, opts); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) DetachSecurityGroup(ctx context.Context, groupID, vmID, interfaceID int64, opts *RequestOpts) error {
	return c.Do(ctx, http.MethodDelete, securityGroupAttachmentPath(groupID, vmID, interfaceID), nil, nil, opts)
}

// Only a scoped application response proves absence. A plain HTTP 404 can mean
// an older server without these endpoints and must not make Terraform forget state.
func IsSecurityGroupNotFound(err error) bool {
	return IsAPIError(err, 703)
}
