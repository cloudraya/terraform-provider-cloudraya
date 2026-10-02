# Packages are offered per region; the lookup fails at plan time if the
# package exists but is not active in region_id.
data "cloudraya_package" "small" {
  name      = "Small-R2"
  region_id = data.cloudraya_region.main.id
}
