# Variables for ProData Provider Configuration

variable "prodata_api_key_id" {
  description = "ProData API Key ID for authentication"
  type        = string
  sensitive   = true
}

variable "prodata_api_secret_key" {
  description = "ProData API Secret Key for authentication"
  type        = string
  sensitive   = true
}

variable "prodata_project_id" {
  description = "ProData Project ID to manage resources in"
  type        = string
}

variable "prodata_region" {
  description = "ProData region for resource deployment"
  type        = string
}
