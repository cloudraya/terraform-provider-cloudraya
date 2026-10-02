# A second subnet in an existing VPC. CloudRaya requires every subnet to have
# an ACL; the one created alongside the VPC is the usual choice.
resource "cloudraya_vpc_network" "app" {
  vpc_id         = cloudraya_vpc.main.id
  network_acl_id = cloudraya_vpc.main.initial_acl_id
  name           = "app"
  description    = "application tier"
  ip_address     = "10.10.33.0"
  network_size   = "24"
}
