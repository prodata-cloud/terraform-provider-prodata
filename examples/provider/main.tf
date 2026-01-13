terraform {
  required_providers {
    prodata = {
      source = "pro-data/prodata"
    }
  }
}

# Configure the ProData provider
provider "prodata" {
  api_base_url   = "https://my.pro-data.tech"
  api_key_id     = "ak_xxxxxxxxxxxxx"
  api_secret_key = "sk_xxxxxxxxxxxxx"
  region         = "UZ-5"
  project_tag    = "your-project-tag"
}

# Alternatively, use environment variables:
# PRODATA_API_BASE_URL
# PRODATA_API_KEY_ID
# PRODATA_API_SECRET_KEY
# PRODATA_REGION
# PRODATA_PROJECT_TAG
