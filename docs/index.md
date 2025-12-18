---
page_title: "ProData Provider"
description: |-
  The ProData provider enables seamless infrastructure management for ProData Cloud resources through Terraform.
---

# ProData Provider

The ProData provider allows you to manage your ProData Cloud infrastructure using Terraform's declarative configuration language. With this provider, you can automate the provisioning, configuration, and lifecycle management of ProData resources.

## Getting Started

To use the ProData provider, you'll need:

- A ProData Cloud account
- API credentials (API Access Key and Secret Key)
- Your project ID and preferred region

## Quick Start Example

```terraform
terraform {
  required_providers {
    prodata = {
      source  = "prodata/prodata"
      version = "~> 1.0"
    }
  }
}

provider "prodata" {
  api_base_url   = "https://api.prodata.cloud"
  api_key_id     = "your-api-key-id"
  api_secret_key = "your-api-secret-key"
  region         = "us-east-1"
  project_id     = "your-project-id"
}
```

## Authentication

The ProData provider supports two authentication methods:

### Method 1: Provider Configuration Block

Configure credentials directly in your Terraform configuration:

```terraform
provider "prodata" {
  api_base_url   = "https://api.prodata.cloud"
  api_key_id     = "prod_key_abc123"
  api_secret_key = "prod_secret_xyz789"
  region         = "us-west-2"
  project_id     = "proj_456def"
}
```

### Method 2: Environment Variables (Recommended)

Set credentials using environment variables for enhanced security:

```bash
export PRODATA_API_BASE_URL="https://api.prodata.cloud"
export PRODATA_API_KEY_ID="prod_key_abc123"
export PRODATA_API_SECRET_KEY="prod_secret_xyz789"
export PRODATA_REGION="us-west-2"
export PRODATA_PROJECT_ID="proj_456def"
```

Then use the provider without explicit credentials:

```terraform
provider "prodata" {
  # Configuration will be loaded from environment variables
}
```

**Best Practice:** Use environment variables or secret management tools (like HashiCorp Vault) to avoid hardcoding sensitive credentials in your Terraform files.

## Configuration Reference

The following arguments are supported in the provider configuration:

### Required Arguments

- **`api_base_url`** (String) - The base URL for the ProData API endpoint.
  *Environment variable:* `PRODATA_API_BASE_URL`
  *Example:* `https://api.prodata.cloud`

- **`api_key_id`** (String) - Your ProData API Key ID used for authentication.
  *Environment variable:* `PRODATA_API_KEY_ID`
  *Example:* `prod_key_abc123`

- **`api_secret_key`** (String, Sensitive) - Your ProData API Secret Key used for authentication.
  *Environment variable:* `PRODATA_API_SECRET_KEY`
  *Security Note:* This value is marked as sensitive and will not appear in logs.

- **`region`** (String) - The ProData Cloud region where resources will be provisioned.
  *Environment variable:* `PRODATA_REGION`
  *Available regions:* `us-east-1`, `us-west-2`, `eu-west-1`, `ap-southeast-1`

- **`project_id`** (String) - The unique identifier for your ProData project.
  *Environment variable:* `PRODATA_PROJECT_ID`
  *Example:* `proj_456def`

## Multiple Provider Instances

You can configure multiple instances of the ProData provider to manage resources across different projects or regions:

```terraform
# Default provider for production
provider "prodata" {
  region     = "us-east-1"
  project_id = "prod-project"
}

# Secondary provider for staging environment
provider "prodata" {
  alias      = "staging"
  region     = "us-west-2"
  project_id = "staging-project"
}

# Use the staging provider explicitly
resource "prodata_example" "staging_resource" {
  provider = prodata.staging
  # ... resource configuration
}
```

## Obtaining API Credentials

To generate API credentials for the ProData provider:

1. Log in to your ProData Cloud console
2. Navigate to **Settings** → **API Keys**
3. Click **Generate New API Key**
4. Copy your API Key ID and Secret Key (the secret will only be shown once)
5. Store your credentials securely

## Regional Availability

ProData Cloud is available in the following regions:

| Region Code      | Location                 | API Endpoint                               |
| ---------------- | ------------------------ | ------------------------------------------ |
| `us-east-1`      | US East (Virginia)       | `https://us-east-1.api.prodata.cloud`      |
| `us-west-2`      | US West (Oregon)         | `https://us-west-2.api.prodata.cloud`      |
| `eu-west-1`      | Europe (Ireland)         | `https://eu-west-1.api.prodata.cloud`      |
| `ap-southeast-1` | Asia Pacific (Singapore) | `https://ap-southeast-1.api.prodata.cloud` |

## Support and Resources

- **Documentation:** [https://docs.prodata.cloud](https://docs.prodata.cloud)
- **API Reference:** [https://api-docs.prodata.cloud](https://api-docs.prodata.cloud)
- **Support Portal:** [https://support.prodata.cloud](https://support.prodata.cloud)
- **Community Forum:** [https://community.prodata.cloud](https://community.prodata.cloud)

## Provider Development

This provider is maintained by the ProData team. For issues, feature requests, or contributions, visit our GitHub repository.
