# InfraLang From a Terraform Perspective

Use this reference when the requested change is described in Terraform terms.
Translate the request to the corresponding InfraLang construct, then preserve
the Terraform address and runtime semantics.

## Core Mapping

| InfraLang source | Terraform HCL equivalent | Terraform JSON shape or reference |
| --- | --- | --- |
| `terraform { requiredVersion: ">= 1.5.0" }` | `terraform { required_version = ">= 1.5.0" }` | `"terraform": { "required_version": ... }` |
| `provider AWS from "hashicorp/aws" version "~> 6.0"` | `required_providers { aws = { source = "hashicorp/aws", version = "~> 6.0" } }` | `terraform.required_providers.aws` |
| `input imageId: string` | `variable "image_id" { type = string }` | `"variable": { "image_id": { "type": "string" } }` |
| `let bucketName = value` | `locals { bucketName = value }` | `"locals": { "bucketName": ... }` |
| `configure aws = AWS({ region })` | `provider "aws" { region = var.region }` | `"provider": { "aws": [{ "region": "${var.region}" }] }` |
| `data item = aws.callerIdentity("current", {})` | `data "aws_caller_identity" "current" {}` | `data.aws_caller_identity.current` |
| `resource bucket = aws.s3Bucket("application", args)` | `resource "aws_s3_bucket" "application" { ... }` | `resource.aws_s3_bucket.application` |
| `module child = Child("stable", args)` | `module "stable" { source = ... }` | `module.stable` |
| `output bucketArn = bucket.arn` | `output "bucketArn" { value = ... }` | `output.bucketArn.value` |
| `moved from "old" to "new"` | `moved { from = "old" to = "new" }` | `moved` entry |

The generated artifact uses Terraform JSON rather than HCL. JSON object keys
are Terraform block categories and labels; expression strings use Terraform's
normal interpolation representation. The HCL column is a mental model for an
AI already familiar with Terraform, not a request to emit HCL.

## References

InfraLang removes Terraform's `var.`, `local.`, and resource-type prefixes from
ordinary source expressions because the declaration kind is already known:

| InfraLang expression | Terraform expression |
| --- | --- |
| `region` | `var.region` |
| `bucketName` when it is a `let` binding | `local.bucketName` |
| `bucket.id` | `aws_s3_bucket.application.id` |
| `account.account_id` for a data handle | `data.aws_caller_identity.current.account_id` |
| `child.endpoint` | `module.stable.endpoint` |
| `each.value.memory` | `each.value.memory` |
| `terraform.data("metadata", ...).output` through a resource handle | `terraform_data.metadata.output` |

Do not manually write `${var.region}` in an ordinary InfraLang string. Use
`region`. Use `f"application-{environment}"` when interpolation is needed.

## Resource Types and Labels

Provider method names are Terraform type names written in camelCase:

```infra
resource bucket = aws.s3Bucket("application", { bucket: name })
data zones = aws.availabilityZones("available", { state: "available" })
```

Terraform sees:

```hcl
resource "aws_s3_bucket" "application" {
  bucket = local.name
}

data "aws_availability_zones" "available" {
  state = "available"
}
```

The source handles `bucket` and `zones` are InfraLang names. The explicit labels
`application` and `available` are Terraform identity. Renaming a source handle
does not rename the Terraform object; changing a label does.

Unquoted argument and metadata keys are converted to snake_case on the wire:

```infra
resource block = aws.someResource("main", {
  blockPublicAcls: true,
}) with {
  dependsOn: [network],
  forEach: instances,
}
```

Terraform receives the conceptual equivalent:

```hcl
resource "aws_some_resource" "main" {
  block_public_acls = true

  depends_on = [aws_network.main]
  for_each   = var.instances
}
```

Quoted object keys preserve exact spelling, which is useful for tag maps and
legacy provider APIs:

```infra
tags: {
  "Name": name,
  "ApplicationName": application,
}
```

The Terraform map keys remain `Name` and `ApplicationName`.

## Providers and Aliases

Terraform separates a provider requirement, a provider configuration, and the
provider argument attached to a resource. InfraLang has the same separation:

```infra
provider AWS from "hashicorp/aws" version "~> 6.0"

configure aws = AWS({ region })
configure awsEast = AWS("east", { region: "us-east-1" })

resource eastBucket = awsEast.s3Bucket("east", {
  bucket: "example-east",
})
```

Terraform's conceptual result is:

```hcl
provider "aws" {
  region = var.region
}

provider "aws" {
  alias  = "east"
  region = "us-east-1"
}

resource "aws_s3_bucket" "east" {
  provider = aws.east
  bucket   = "example-east"
}
```

The local handle `awsEast` is not the Terraform alias text. The alias is the
explicit string `"east"`. Preserve aliases when refactoring because provider
configuration identity can affect state.

For a child module, `using` corresponds to Terraform's provider mapping:

```infra
module child = Child("child", {}) using {
  aws: awsEast,
}
```

Conceptually:

```hcl
module "child" {
  source = "..."

  providers = {
    aws = aws.east
  }
}
```

An inherited child declaration such as `configure aws = AWS` corresponds to a
child module that expects the provider configuration from its caller, not to a
new provider block.

## Modules and Components

A module import is a compile-time name binding. It is not Terraform's module
block by itself:

```infra
import module Web from "./modules/web"
module web = Web("production", {
  hostname,
}) using { aws }
```

Terraform sees one module block with `source = "./modules/web"` and the address
`module.production`. The InfraLang instance handle is `web`; the Terraform
label is `production`.

A component is different from a module. Terraform sees the component's expanded
resources directly in the caller:

```infra
component Bucket(label: string, name: string) using { aws: AWS } {
  resource bucket = aws.s3Bucket(label, { bucket: name })
  export arn = bucket.arn
}

instantiate app = Bucket(label: "application", name: bucketName) using { aws }
output appArn = app.arn
```

There is no `module.*` address for `app`; Terraform sees the expanded resource,
for example `aws_s3_bucket.application`. Component exports are typed handles,
not Terraform outputs, until the caller declares `output appArn`.

## Iteration and Conditionals

InfraLang has both compile-time and Terraform-runtime iteration:

| InfraLang | Terraform meaning |
| --- | --- |
| `static for ...` | compiler expands a fixed set of declarations; no `for_each` is emitted |
| resource/module `with { forEach: values }` | Terraform `for_each = values`; `each` is runtime |
| `resource ... when enabled` | Terraform `count = enabled ? 1 : 0` |
| indexed provider/module/component handle | compile-time lookup table; no Terraform index mechanism by itself |

Example runtime resource iteration:

```infra
resource vm = libvirt.domain("worker", {
  name: each.key,
  memory: each.value.memory,
}) with {
  forEach: workers,
}
```

Terraform conceptually sees:

```hcl
resource "libvirt_domain" "worker" {
  for_each = var.workers
  name     = each.key
  memory   = each.value.memory
}
```

Because Terraform manages instances by keys, changing runtime keys can replace
state addresses. A static loop instead creates distinct declarations before
Terraform sees the configuration and usually uses explicit labels per item.

## State-Preserving Review

When reviewing an InfraLang change, inspect the same identity inputs you would
inspect in Terraform:

- provider source and provider alias;
- resource or data type and explicit label;
- module explicit label and source;
- runtime `count` or `for_each` keys;
- static keys used to produce provider aliases or labels.

These source-only names do not independently change Terraform identity:

- resource/data/module source handles;
- type aliases and type-only import names;
- constants and static iterator names;
- component names, parameters, instance handles, and virtual exports.

InfraLang does not infer Terraform state moves. If the desired address changes,
write a `moved` declaration using raw Terraform addresses and confirm the plan.

## What Terraform Still Validates

After InfraLang compilation succeeds, Terraform/OpenTofu still must validate:

- provider resource and data source schemas;
- nested block shapes and provider-specific arguments;
- provider installation and version resolution;
- remote module interfaces;
- Terraform function behavior and unknown-value semantics;
- plan-time graph behavior and state changes.
