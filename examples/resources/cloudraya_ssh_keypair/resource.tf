# CloudRaya has no update call for keypairs, so changing any argument replaces
# the keypair.
resource "cloudraya_ssh_keypair" "deploy" {
  name       = "deploy-key"
  public_key = file("~/.ssh/id_ed25519.pub")
}
