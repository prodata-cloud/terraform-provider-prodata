terraform {
  required_providers {
    prodata = {
      source = "pro-data/prodata"
    }
  }
}

provider "prodata" {
  # Configuration options
}

# Create an HDD volume
resource "prodata_volume" "main" {
  region     = "UZ-5"
  project_id = 89
  name       = "terraform-volume"
  type       = "HDD"
  size       = 10
}

output "volume" {
  value = {
    id   = prodata_volume.main.id
    name = prodata_volume.main.name
    type = prodata_volume.main.type
    size = prodata_volume.main.size
  }
}

# Create an SSD volume
resource "prodata_volume" "ssd" {
  region     = "UZ5"
  project_id = 89
  name       = "fast-storage"
  type       = "SSD"
  size       = 20
}
