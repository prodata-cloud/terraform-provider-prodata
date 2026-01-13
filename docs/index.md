---
page_title: "Provider: ProData"
description: |-
  The ProData provider enables Terraform to manage ProData Cloud resources.
---

# ProData Provider

The ProData provider enables Terraform to manage [ProData Cloud](https://pro-data.tech) infrastructure.

## Example Usage

### Using Environment Variables (Recommended)

```bash
export PRODATA_API_BASE_URL="https://my.pro-data.tech"
export PRODATA_API_KEY_ID="your-api-key-id"
export PRODATA_API_SECRET_KEY="your-api-secret-key"
export PRODATA_REGION="UZ-5"
export PRODATA_PROJECT_TAG="your-project-tag"
```

```terraform
terraform {
  required_providers {
    prodata = {
      source  = "prodata-cloud/prodata"
      version = "~> 0.1"
    }
  }
}

provider "prodata" {}
```

### Using Provider Configuration

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
  region         = "UZ-5"
  project_tag    = "your-project-tag"
}
```

-> **Note:** Configuration values take precedence over environment variables.

## Authentication

Obtain API credentials from the ProData Cloud console:

1. Log in to your ProData Cloud console
2. Navigate to **Account** > **Access Keys**
3. Click **Generate Key**
4. Save the API Key ID and Secret Key (shown only once)

## Schema

### Optional

- `api_base_url` (String) ProData API base URL (e.g., `https://my.pro-data.tech`). Can also be set via `PRODATA_API_BASE_URL` environment variable. **Required for provider to function.**
- `api_key_id` (String) API Key ID for authentication. Can also be set via `PRODATA_API_KEY_ID` environment variable. **Required for provider to function.**
- `api_secret_key` (String, Sensitive) API Secret Key for authentication. Can also be set via `PRODATA_API_SECRET_KEY` environment variable. **Required for provider to function.**
- `region` (String) Default region ID (e.g., `UZ-5`, `UZ-3`, `KZ-1`). Can also be set via `PRODATA_REGION` environment variable.
- `project_tag` (String) Default project tag. Can also be set via `PRODATA_PROJECT_TAG` environment variable.

## Regional API URLs

| Region     | Base URL                     |
| ---------- | ---------------------------- |
| Uzbekistan | `https://my.pro-data.tech`   |
| Kazakhstan | `https://kz-1.pro-data.tech` |

## Support

- **Help Desk**: [helpdesk.pro-data.tech](https://helpdesk.pro-data.tech)
- **Telegram**: [@PRO_DATA_Support_Bot](https://t.me/PRO_DATA_Support_Bot)
