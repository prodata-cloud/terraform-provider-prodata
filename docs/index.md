---
page_title: "ProData Provider"
description: |-
  Manage ProData Cloud resources with Terraform.
---

# ProData Provider

Manage ProData Cloud infrastructure using Terraform.

## Example Usage

```terraform
terraform {
  required_providers {
    prodata = {
      source  = "prodata-cloud/prodata"
      version = "~> 0.1"
    }
  }
}

provider "prodata" {
  api_base_url   = "https://my.pro-data.tech"
  api_key_id     = "your-api-key-id"
  api_secret_key = "your-api-secret-key"
  region         = "your-region(UZ-5/UZ-3/KZ-1)"
  project_id     = 123
}

```

## Authentication

### Using Environment Variables (Recommended)

```bash
export PRODATA_API_BASE_URL="https://my.pro-data.tech"
export PRODATA_API_KEY_ID="your-api-key-id"
export PRODATA_API_SECRET_KEY="your-api-secret-key"
```

```terraform
provider "prodata" {}
```

### Using Provider Configuration

```terraform
provider "prodata" {
  api_base_url   = "https://my.pro-data.tech"
  api_key_id     = "your-api-key-id"
  api_secret_key = "your-api-secret-key"
  region         = "your-region(UZ-5/UZ-3/KZ-1)"
  project_id     = 123
}
```

## Schema

### Required

All arguments are technically optional but required for the provider to function. They can be set via environment variables or in the configuration block.

- `api_base_url` (String) ProData API base URL. See [Regional URLs](#regional-urls) below. Env: `PRODATA_API_BASE_URL`
- `api_key_id` (String) API Key ID for authentication. Env: `PRODATA_API_KEY_ID`
- `api_secret_key` (String, Sensitive) API Secret Key for authentication. Env: `PRODATA_API_SECRET_KEY`
- `region` (String) Default region ID (e.g., "1", "2", "3"). Env: `PRODATA_REGION`
- `project_id` (Number) Project ID. Env: `PRODATA_PROJECT_ID`

## Regional URLs

Use the appropriate base URL for your region:

| Region     | Base URL                     |
| ---------- | ---------------------------- |
| Uzbekistan | `https://my.pro-data.tech`   |
| Kazakhstan | `https://kz-1.pro-data.tech` |

## Getting API Credentials

1. Log in to your ProData Cloud console (see [Regional URLs](#regional-urls))
2. Go to **Account** → **Access Keys**
3. Click **Generate Key**
4. Save your API Key ID and Secret (shown only once)

## Support

Need help? Contact ProData support:

- **Help Desk**: [helpdesk.pro-data.tech](https://helpdesk.pro-data.tech)
- **Telegram**: [@PRO_DATA_Support_Bot](https://t.me/PRO_DATA_Support_Bot)
