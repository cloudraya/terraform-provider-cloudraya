# Packages are offered per region; the lookup fails at plan time if the
# package exists but is not active in region_id.
data "cloudraya_package" "small" {
  name      = "small-free-1-IP"
  region_id = data.cloudraya_region.main.id
}
