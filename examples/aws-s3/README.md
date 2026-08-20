# AWS S3 example

This example creates a private S3 bucket in AWS. The bucket name includes the
current AWS account ID so that it is globally unique. Public access is blocked,
object versioning is enabled, and new objects use server-side AES-256
encryption by default.

Authenticate with AWS using the standard AWS provider mechanisms, for example
an `AWS_PROFILE`, environment variables, or an instance role. Then run from the
repository root:

```shell
bin/infralang check examples/aws-s3/main.infra
bin/infralang build examples/aws-s3/main.infra
terraform -chdir=examples/aws-s3 init -backend=false
terraform -chdir=examples/aws-s3 plan
terraform -chdir=examples/aws-s3 apply
```

The defaults create
`infralang-example-development-<AWS_ACCOUNT_ID>` in `eu-central-1`. Override
the inputs with Terraform variables, for example:

```shell
terraform -chdir=examples/aws-s3 plan \
  -var='region=eu-west-1' \
  -var='project=my-application' \
  -var='environment=staging'
```

Bucket names must use only lowercase letters, numbers, periods, and hyphens.
Choose lowercase `project` and `environment` values. To remove the example,
first delete any objects and object versions from the bucket, then run:

```shell
terraform -chdir=examples/aws-s3 destroy
```
