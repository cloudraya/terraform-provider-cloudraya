# A VPC network (subnet) is imported as "<vpc_id>/<network_id>", because every
# subnet is addressed under its parent VPC.
terraform import cloudraya_vpc_network.app <vpc_id>/<network_id>
