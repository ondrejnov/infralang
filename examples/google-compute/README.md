# Google Cloud VPS example

This example creates a small Compute Engine VPS in Google Cloud with a custom
VPC, subnet, static external IPv4 address, Debian boot disk, and an SSH-only
firewall rule.

Authenticate with Google Cloud using Application Default Credentials, for
example with `gcloud auth application-default login` or a service account
configured through the standard Google provider mechanisms. Enable the Compute
Engine API in the selected project, then run from the repository root:

```shell
bin/infralang check examples/google-compute/main.infra
bin/infralang build examples/google-compute/main.infra
terraform -chdir=examples/google-compute init -backend=false
terraform -chdir=examples/google-compute plan \
  -var='project_id=YOUR_PROJECT_ID'
terraform -chdir=examples/google-compute apply \
  -var='project_id=YOUR_PROJECT_ID'
```

The default SSH rule allows `0.0.0.0/0` for demonstration purposes. Restrict
it to your own public IP before applying to a real project:

```shell
terraform -chdir=examples/google-compute plan \
  -var='project_id=YOUR_PROJECT_ID' \
  -var='ssh_source_ranges=["203.0.113.10/32"]'
```

Destroy the VPS and its network resources with:

```shell
terraform -chdir=examples/google-compute destroy \
  -var='project_id=YOUR_PROJECT_ID'
```
