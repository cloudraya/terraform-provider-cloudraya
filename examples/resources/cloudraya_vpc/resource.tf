data "cloudraya_region" "main" {
  name = "Jakarta-Edge2-LIVE"
}

# Creating a VPC also provisions its first subnet and ACL. Manage further
# subnets with cloudraya_vpc_network.
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
