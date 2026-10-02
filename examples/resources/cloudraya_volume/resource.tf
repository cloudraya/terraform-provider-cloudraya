data "cloudraya_vm_storage_package" "disk" {
  name = "Disk-50"
}

# A data disk attached to an existing virtual machine. Leave
# virtual_machine_id unset for a detached volume.
resource "cloudraya_volume" "data" {
  name               = "web-01-data"
  region_id          = cloudraya_virtual_machine.web.region_id
  product_id         = data.cloudraya_vm_storage_package.disk.id
  virtual_machine_id = cloudraya_virtual_machine.web.id
}
