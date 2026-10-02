# Terraform Provider for CloudRaya

A [Terraform](https://www.terraform.io) provider for [CloudRaya](https://cloudraya.com):
virtual machines, block storage, VPCs, subnets and SSH keypairs, managed as code
through the [CloudRaya API](https://developers.cloudraya.com).

> **Status:** early release. The provider is not yet published to the Terraform
> Registry; build it from source as described below.

## Resources and data sources

| Resource | Manages |
|---|---|
| [`cloudraya_virtual_machine`](docs/resources/virtual_machine.md) | Virtual machines — deploy, rename, resize, change network or SSH keys, destroy |
| [`cloudraya_volume`](docs/resources/volume.md) | Block-storage data disks — create, resize, attach and detach, destroy |
| [`cloudraya_vpc`](docs/resources/vpc.md) | VPCs, together with their first subnet and ACL |
| [`cloudraya_vpc_network`](docs/resources/vpc_network.md) | Additional subnets inside a VPC |
| [`cloudraya_ssh_keypair`](docs/resources/ssh_keypair.md) | SSH public keys authorised on VMs |

| Data source | Looks up |
|---|---|
| [`cloudraya_region`](docs/data-sources/region.md) | A region by display name |
| [`cloudraya_package`](docs/data-sources/package.md) | A VM package (size) offered in a region |
| [`cloudraya_template`](docs/data-sources/template.md) | An OS template offered in a region |
| [`cloudraya_vm_storage_package`](docs/data-sources/vm_storage_package.md) | A data-disk package |
| [`cloudraya_vpc_network`](docs/data-sources/vpc_network.md) | An existing subnet by name |

The data sources let a configuration refer to everything by name instead of
copying IDs around. Package and template lookups also check that the item is
actually offered in the chosen region, so a mismatch fails at `terraform plan`
with a clear message rather than partway through an apply.

Full reference documentation for every argument and attribute is in [`docs/`](docs/).

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) 1.0 or later
- [Go](https://go.dev/doc/install) 1.27 or later, to build the provider
- A CloudRaya account and a project to provision into

## Installation

Build the provider and install it into your `GOBIN`:

```sh
git clone https://github.com/cloudraya/terraform-provider-cloudraya.git
cd terraform-provider-cloudraya
go install .
```

Then point Terraform at the local build by adding a `dev_overrides` block to
`~/.terraformrc`, replacing the path with your `GOBIN` (usually `~/go/bin`):

```hcl
provider_installation {
  dev_overrides {
    "cloudraya/cloudraya" = "/Users/you/go/bin"
  }
  direct {}
}
```

With an override in place, skip `terraform init` and run `terraform plan` or
`terraform apply` directly. Terraform prints a warning that the override is
active; that is expected.

## Authentication

The provider signs in with your CloudRaya account e-mail and password,
exchanges them for a bearer token, and refreshes the token before it expires.
Credentials can be set in the provider block, but environment variables keep
them out of your configuration:

```sh
export CLOUDRAYA_EMAIL="you@example.com"
export CLOUDRAYA_PASSWORD="your-password"
export CLOUDRAYA_PROJECT_ID="your-project-id"   # optional default project
```

| Argument | Environment variable | Description |
|---|---|---|
| `email` | `CLOUDRAYA_EMAIL` | Account e-mail |
| `password` | `CLOUDRAYA_PASSWORD` | Account password |
| `project_id` | `CLOUDRAYA_PROJECT_ID` | Default project for resources that don't set their own |
| `base_url` | `CLOUDRAYA_BASE_URL` | API host; defaults to `https://api-v2.cloudraya.com` |

## Example

A VM with an SSH key and an attached data disk, with every ID looked up by name:

```hcl
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
  name = "Jakarta"
}

data "cloudraya_package" "small" {
  name      = "Small-R2"
  region_id = data.cloudraya_region.main.id
}

data "cloudraya_template" "ubuntu" {
  name      = "Ubuntu 22.04 v05.22"
  region_id = data.cloudraya_region.main.id
}

data "cloudraya_vpc_network" "net" {
  name = "app-subnet"
}

data "cloudraya_vm_storage_package" "disk" {
  name = "Disk-50"
}

resource "cloudraya_ssh_keypair" "deploy" {
  name       = "deploy-key"
  public_key = file("~/.ssh/id_ed25519.pub")
}

resource "cloudraya_virtual_machine" "web" {
  hostname        = "web-01"
  region_id       = data.cloudraya_region.main.id
  package_id      = data.cloudraya_package.small.id
  template_id     = data.cloudraya_template.ubuntu.id
  network_id      = data.cloudraya_vpc_network.net.id
  ssh_keypair_ids = [cloudraya_ssh_keypair.deploy.id]
}

resource "cloudraya_volume" "data" {
  name               = "web-01-data"
  region_id          = data.cloudraya_region.main.id
  product_id         = data.cloudraya_vm_storage_package.disk.id
  virtual_machine_id = cloudraya_virtual_machine.web.id
}
```

A runnable version with variables is in [`examples/complete`](examples/complete),
and every resource and data source has its own example under
[`examples/`](examples/).

## Behaviour worth knowing

- **Provisioning is asynchronous.** CloudRaya accepts a create request and then
  builds the resource in the background. The provider waits until the resource
  is active before reporting success, so a VM typically takes one to three
  minutes to apply and a VPC around a minute and a half.
- **One plan can become several API calls.** CloudRaya has no single "update
  VM" call. Renaming and resizing a VM in the same apply sends one request per
  change.
- **SSH keypairs are replaced, not edited.** The API has no update call for
  keypairs, so changing a keypair's name or key destroys and recreates it.
- **Volumes are detached before they are destroyed**, so destroying a VM and
  its data disk together works in a single `terraform destroy`.

## Importing existing resources

Every resource can be imported. Most take their ID:

```sh
terraform import cloudraya_virtual_machine.web <vm_id>
```

Subnets are addressed under their VPC, so they take both IDs:

```sh
terraform import cloudraya_vpc_network.app <vpc_id>/<network_id>
```

The first `terraform apply` after an import may show a few in-place updates and
no replacements. These updates record values the API only returns at creation
time, such as a VPC's `initial_subnet` and `initial_acl` blocks. CloudRaya does
not report which SSH keypairs a VM has, so that same apply also re-applies the
VM's configured `ssh_keypair_ids`. After it, the plan is clean.

## Development

```sh
go build ./...
go vet ./...
go test ./...
```

`go test ./...` runs unit tests against a local mock of the API. Live tests
against a real account run only when credentials are exported, and are skipped
otherwise:

```sh
CLOUDRAYA_EMAIL=... CLOUDRAYA_PASSWORD=... go test ./internal/client/ -run TestLive -v
```

The files under [`docs/`](docs/) are generated from the provider's schema and
the files under [`examples/`](examples/). After changing either, regenerate
them:

```sh
go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.21.0 generate \
  --provider-name cloudraya --rendered-provider-name CloudRaya
```

## Contributing

Issues and pull requests are welcome. Please run `go vet ./...` and
`go test ./...` before opening a pull request, and regenerate the docs if you
change a schema.

## License

[Mozilla Public License 2.0](LICENSE)
