data "cloudraya_region" "main" {
  name = "Jakarta-Edge2-LIVE"
}

data "cloudraya_package" "small" {
  name      = "small-free-1-IP"
  region_id = data.cloudraya_region.main.id
}

data "cloudraya_template" "ubuntu" {
  name      = "Ubuntu 22.04 v05.22"
  region_id = data.cloudraya_region.main.id
}

resource "cloudraya_ssh_keypair" "deploy" {
  name       = "deploy-key"
  public_key = file("~/.ssh/id_ed25519.pub")
}

resource "cloudraya_vpc" "main" {
  name         = "main"
  region_id    = data.cloudraya_region.main.id
  ip_address   = "10.40.0.0"
  network_size = "19"

  initial_subnet = {
    name         = "main-subnet"
    ip_address   = "10.40.0.0"
    network_size = "25"
  }

  initial_acl = {
    name = "main-acl"
    rules = [{
      protocol     = "TCP"
      action       = "Allow"
      source_cidr  = "0.0.0.0/0"
      traffic_type = "Ingress"
      start_port   = "22"
      end_port     = "22"
    }]
  }
}

resource "cloudraya_virtual_machine" "web" {
  hostname        = "web-01"
  region_id       = data.cloudraya_region.main.id
  package_id      = data.cloudraya_package.small.id
  template_id     = data.cloudraya_template.ubuntu.id
  network_id      = cloudraya_vpc.main.initial_subnet_id
  ssh_keypair_ids = [cloudraya_ssh_keypair.deploy.id]
  note            = "managed by terraform"
}
