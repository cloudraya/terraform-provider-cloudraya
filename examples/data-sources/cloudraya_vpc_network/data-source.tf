# Finds an existing subnet by name across every VPC in the project.
data "cloudraya_vpc_network" "existing" {
  name = "app-subnet"
}
