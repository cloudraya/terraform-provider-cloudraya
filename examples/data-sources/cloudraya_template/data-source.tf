# Templates are offered per region; the lookup fails at plan time if the
# template exists but is not active in region_id.
data "cloudraya_template" "ubuntu" {
  name      = "Ubuntu 22.04 v05.22"
  region_id = data.cloudraya_region.main.id
}
