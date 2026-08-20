# LVM example

This example uses the local `github.com/ondrejnov/lvm` provider to create and
grow a regular logical volume on `10.0.0.2` over SSH. The provider does not
install LVM2 or create physical volumes, volume groups, filesystems, or mounts.

Install version `0.5.0` of the provider into Terraform's local plugin mirror as
described in `/var/www/terraform-provider-lvm/provider-lvm/README.md`, then run:

```shell
bin/infralang check examples/lvm
bin/infralang build examples/lvm
terraform -chdir=examples/lvm init -backend=false
terraform -chdir=examples/lvm plan
```

The target volume group must already exist on `10.0.0.2`. The typed SSH input is
defined in `ssh.infra`; its default remote user is `root`. Authentication and
host-key verification use the SSH agent, `~/.ssh/config`, and standard
known-hosts files. Increasing `sizeGiB` extends
the logical volume, but does not resize its filesystem. Shrinking is rejected.

The example sets `preventDestroy: true` because replacing or destroying the
resource permanently removes the block device. Remove that lifecycle setting
only when deletion is intentional.

To use explicit SSH files, set the optional fields in `ssh.infra`:

```infra
input ssh: SshConfig = {
  host: "10.0.0.2",
  user: "root",
  identityFile: "/home/example/.ssh/id_ed25519",
  knownHostsFile: "/home/example/.ssh/known_hosts",
}
```
