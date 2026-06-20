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
      version = "~> 0.21"
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
      version = "~> 0.21"
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

## Sensitive data and Terraform state

Several attributes are marked `Sensitive`, which redacts them from `terraform plan`/`apply`
console output — but **`Sensitive` does not encrypt Terraform state**. These values are
written to the state file (and any plan file) in plaintext:

- `api_secret_key` (provider configuration)
- `prodata_vm`: `password`
- `prodata_kubernetes_cluster`: the entire `kube_config` block (client certificate, client
  key, bearer `token`, `raw_config`), plus `ssh_key_encoded` and `private_key_encoded`

Treat the state file as a secret. Use a remote backend with **encryption at rest and access
controls** (for example an encrypted object-storage backend), restrict who can read it, and
avoid committing `terraform.tfstate` to source control. Write-only attributes that are never
read back (`prodata_vm` `password`, `ssh_public_key`, and `user_data`) are not stored in
state at all.

## Schema

### Optional

- `api_base_url` (String) ProData API base URL (e.g., `https://my.pro-data.tech`). Can also be set via `PRODATA_API_BASE_URL` environment variable. **Required for provider to function.**
- `api_key_id` (String) API Key ID for authentication. Can also be set via `PRODATA_API_KEY_ID` environment variable. **Required for provider to function.**
- `api_secret_key` (String, Sensitive) API Secret Key for authentication. Can also be set via `PRODATA_API_SECRET_KEY` environment variable. **Required for provider to function.**
- `region` (String) Default region ID (e.g., `UZ-5`, `UZ-3`, `KZ-1`). Can also be set via `PRODATA_REGION` environment variable.
- `project_tag` (String) Default project tag. Can also be set via `PRODATA_PROJECT_TAG` environment variable. The tag is shown on the project's settings page in the ProData Console; if you need to construct it manually, the format is `lowercase(name).replace(' ', '-') + '-' + id` — for example, a project named "My Project" with numeric id `42` has tag `my-project-42`.

## Regional API URLs

| Region     | Base URL                     |
| ---------- | ---------------------------- |
| Uzbekistan | `https://my.pro-data.tech`   |
| Kazakhstan | `https://kz-1.pro-data.tech` |

## Performance tuning

The provider already retries HTTP 429 (rate-limited) responses with backoff. On very large
applies you can additionally cap the outbound request rate to pre-empt rate limiting by
setting the `PRODATA_MAX_RPS` environment variable to the maximum requests per second
(e.g. `export PRODATA_MAX_RPS=5`). Unset or `0` (the default) disables client-side pacing.

## Support

- **Help Desk**: [helpdesk.pro-data.tech](https://helpdesk.pro-data.tech)
- **Telegram**: [@PRO_DATA_Support_Bot](https://t.me/PRO_DATA_Support_Bot)
