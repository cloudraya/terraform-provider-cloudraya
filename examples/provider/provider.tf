terraform {
  required_providers {
    cloudraya = {
      source = "cloudraya/cloudraya"
    }
  }
}

# Credentials are read from CLOUDRAYA_EMAIL and CLOUDRAYA_PASSWORD when not set
# here. Prefer the environment variables over committing credentials.
provider "cloudraya" {
  project_id = var.project_id
}

variable "project_id" {
  description = "CloudRaya project to manage resources in."
  type        = string
}
