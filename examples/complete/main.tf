# A VM with an SSH key and an attached data disk, with every ID looked up by
# name. Credentials come from CLOUDRAYA_EMAIL and CLOUDRAYA_PASSWORD.
#
#   export CLOUDRAYA_EMAIL=you@example.com
#   export CLOUDRAYA_PASSWORD=...
#   terraform init && terraform apply -var project_id=<project_id>

terraform {
  required_providers {
    cloudraya = {
      source = "cloudraya/cloudraya"
    }
  }
}

provider "cloudraya" {
  project_id = var.project_id
}

data "cloudraya_region" "main" {
  name = var.region_name
}

data "cloudraya_package" "vm" {
  name      = var.package_name
  region_id = data.cloudraya_region.main.id
}

data "cloudraya_template" "os" {
  name      = var.template_name
  region_id = data.cloudraya_region.main.id
}

data "cloudraya_vpc_network" "net" {
  name = var.network_name
}

data "cloudraya_vm_storage_package" "disk" {
  name = var.disk_package_name
}

resource "cloudraya_ssh_keypair" "deploy" {
  name       = "${var.hostname}-key"
  public_key = file(pathexpand(var.ssh_public_key_path))
}

resource "cloudraya_virtual_machine" "web" {
  hostname        = var.hostname
  region_id       = data.cloudraya_region.main.id
  package_id      = data.cloudraya_package.vm.id
  template_id     = data.cloudraya_template.os.id
  network_id      = data.cloudraya_vpc_network.net.id
  ssh_keypair_ids = [cloudraya_ssh_keypair.deploy.id]
  note            = "managed by terraform"
}

resource "cloudraya_volume" "data" {
  name               = "${var.hostname}-data"
  region_id          = data.cloudraya_region.main.id
  product_id         = data.cloudraya_vm_storage_package.disk.id
  virtual_machine_id = cloudraya_virtual_machine.web.id
}

output "vm_id" {
  value = cloudraya_virtual_machine.web.id
}

output "vm_state" {
  value = cloudraya_virtual_machine.web.state
}

output "data_disk_gb" {
  value = cloudraya_volume.data.disk_size_gb
}
