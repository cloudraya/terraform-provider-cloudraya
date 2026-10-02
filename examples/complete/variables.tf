variable "project_id" {
  description = "CloudRaya project to provision into."
  type        = string
}

variable "region_name" {
  description = "Region display name."
  type        = string
  default     = "Jakarta"
}

variable "package_name" {
  description = "VM package (size) name. Must be offered in the region."
  type        = string
  default     = "Small-R2"
}

variable "template_name" {
  description = "OS template name. Must be offered in the region."
  type        = string
  default     = "Ubuntu 22.04 v05.22"
}

variable "network_name" {
  description = "Existing VPC subnet to attach the VM to."
  type        = string
}

variable "disk_package_name" {
  description = "Data-disk package name."
  type        = string
  default     = "Disk-50"
}

variable "hostname" {
  description = "VM hostname; also used to name the keypair and data disk."
  type        = string
  default     = "web-01"
}

variable "ssh_public_key_path" {
  description = "Public key to authorise on the VM."
  type        = string
  default     = "~/.ssh/id_ed25519.pub"
}
