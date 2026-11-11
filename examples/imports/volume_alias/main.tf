terraform {
  required_providers {
    prodata = {
      source = "prodata-cloud/prodata"
    }
  }
}

# Configure PRODATA_API_BASE_URL, PRODATA_API_KEY_ID and PRODATA_API_SECRET_KEY.
# Replace these sample IDs and volume attributes with the existing resources.
provider "prodata" {
  alias       = "target"
  region      = "UZ-5"
  project_tag = "target-project"
}

resource "prodata_volume" "existing" {
  provider = prodata.target
  name     = "existing-volume"
  type     = "SSD"
  size     = 10
}

resource "prodata_volume_attachment" "existing" {
  provider  = prodata.target
  vm_id     = 123
  volume_id = prodata_volume.existing.id
}

import {
  to = prodata_volume.existing
  id = "456"
}

import {
  to = prodata_volume_attachment.existing
  id = "123:456"
}
