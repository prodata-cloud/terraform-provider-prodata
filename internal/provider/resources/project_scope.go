package resources

import (
	"context"
	"fmt"

	"terraform-provider-prodata/internal/client"
)

// requireClusterInProject checks the project-scoped list because older API
// versions return clusters and node pools by organization alone on by-ID reads.
// A pool or CCM load balancer inherits its parent cluster's project; passing a
// different header cannot move it. With no configured project, preserve that
// inheritance without introducing a new project-list requirement.
func requireClusterInProject(ctx context.Context, c *client.Client, clusterID int64, opts *client.RequestOpts) error {
	if opts == nil || opts.ProjectTag == "" {
		return nil
	}
	clusters, err := c.ListClusters(ctx, opts)
	if err != nil {
		return fmt.Errorf("could not verify cluster %d in selected project %q: %w. "+
			"Verify Kubernetes read permissions for the API key before retrying", clusterID, opts.ProjectTag, err)
	}
	for _, cluster := range clusters {
		if cluster.ID == clusterID {
			return nil
		}
	}
	return fmt.Errorf("cluster %d is outside the selected project %q and region %q, or is no longer available. "+
		"Set project_tag and region to match the parent cluster before creating this resource", clusterID, opts.ProjectTag, opts.Region)
}
