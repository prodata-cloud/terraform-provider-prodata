// Copyright (c) HashiCorp, Inc.
// SPDX-License-Identifier: MPL-2.0

//go:build generate

package tools

import (
	_ "github.com/hashicorp/copywrite"
	_ "github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs"
)

// Generate copyright headers
//go:generate go run github.com/hashicorp/copywrite headers -d .. --config ../.copywrite.hcl

// Format Terraform code for use in documentation.
// If you do not have Terraform installed, you can remove the formatting command, but it is suggested
// to ensure the documentation is formatted properly.
//go:generate terraform fmt -recursive ../examples/

// Validate the hand-written docs against the provider schema, and enforce the subcategory allowlist.
// Do NOT switch this to `tfplugindocs generate`: ../docs is authored by hand (prose, multiple examples,
// custom sections) and generate would overwrite that curated content with the auto-generated skeleton.
//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs validate --provider-dir .. --provider-name prodata --allowed-resource-subcategories "Compute,Storage,Networking,Load Balancer"
