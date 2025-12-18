terraform {
  required_providers {
    prodata = {
      source  = "prodata/prodata"
      version = "~> 1.0"
    }
  }
}

# Configure the ProData Provider
provider "prodata" {
  api_base_url   = "https://my.pro-data.tech Or https://kz-1.pro-data.tech"
  api_key_id     = var.prodata_api_key_id
  api_secret_key = var.prodata_api_secret_key
  region         = "UZ-5/UZ-3/KZ-1"
  project_id     = var.prodata_project_id
}

# Alternative: Using environment variables (recommended)
# Set these environment variables before running Terraform:
# export PRODATA_API_BASE_URL="https://my.pro-data.cloud"
# export PRODATA_API_KEY_ID="your-api-key-id"
# export PRODATA_API_SECRET_KEY="your-api-secret-key"
# export PRODATA_REGION="UZ-5/UZ-3/KZ-1"
# export PRODATA_PROJECT_ID="your-project-id"

# provider "prodata" {
#   # Configuration loaded from environment variables
# }
