# InfraLang

InfraLang is a statically checked, programmer-oriented language that compiles to
Terraform JSON.
It preserves Terraform and OpenTofu providers, modules, dependency graphs,
state, planning, and apply behavior instead of replacing the ecosystem.

[Language reference](docs/language.md) · [Releases](https://github.com/ondrejnov/infralang/releases) · [VS Code extension](vscode-infralang/)

> [!WARNING]
> InfraLang is an experimental MVP. Use it to explore the language and generate
> Terraform configuration, but do not use it for production infrastructure yet.

## Quick start

### Requirements

- Go 1.24 or newer to build InfraLang from source
- Terraform or OpenTofu for initialization, validation, planning, and apply
- The providers required by the example or your own configuration

Build the CLI from a checkout:

```shell
git clone https://github.com/ondrejnov/infralang.git
cd infralang
mkdir -p bin
go build -o bin/infralang ./cmd/infralang
```

Prebuilt binaries for Linux, macOS, and Windows are available on the
[Releases](https://github.com/ondrejnov/infralang/releases) page.

Check and build an example:

```shell
bin/infralang check examples/basic/main.infra
bin/infralang build examples/basic/main.infra
```

The build writes `examples/basic/main.tf.json`. To inspect the generated JSON
without writing a file, use `bin/infralang build -stdout examples/basic/main.infra`.

Run Terraform from an example directory as usual:

```shell
cd examples/basic
../../bin/infralang init -backend=false
../../bin/infralang validate
../../bin/infralang plan -input=false
```

For a first AWS example, see [`examples/aws-s3`](examples/aws-s3/). It requires
the HashiCorp AWS provider and valid AWS credentials.

## Features

- Provider source and version declarations
- Aliased provider configurations
- Typed inputs and inferred local values
- Terraform functions and expression operators
- Formatted strings using `f"host-{name}"`
- Provider resources with stable Terraform labels
- Provider data sources
- Existing Terraform modules as first-class values
- Recursive builds of local InfraLang modules
- Structural object inputs with optional field defaults and validations
- List and object comprehensions
- Terraform module meta-arguments and inherited provider handles
- State move declarations
- Outputs and references between inputs, locals, resources, and modules
- Basic static type and name checking
- Terraform JSON generation with atomic output replacement
- Source diagnostics with line and column information
- Input source/wire aliases, object punning, concise validations, grouped raw moves, and conditional resources
- Structural type aliases, ordered object spreads, conditional fields, and checked local module interfaces
- Checked input forwarding with `...inputs(value)` and provider shorthand mappings
- Exact compile-time constants, deterministic static declaration loops, and compile-time labels
- Canonical type-only imports and directory-scoped reusable components
- Indexed provider, module, and component handles that erase before Terraform lowering

See the [language reference](docs/language.md) for syntax, type rules, imports,
components, modules, and Terraform lowering details.

## Why move from Terraform HCL to InfraLang?

Terraform is an excellent execution engine, but HCL becomes increasingly
awkward as infrastructure grows into a software project. InfraLang replaces the
authoring language, not the proven runtime: it compiles statically checked
`.infra` files to ordinary Terraform JSON, then leaves provider execution,
dependency ordering, state, plan, and apply to Terraform or OpenTofu.

This gives infrastructure code the abstractions and feedback developers expect
from a programming language without giving up the Terraform ecosystem:

| Terraform HCL | InfraLang |
| --- | --- |
| References expose declaration categories: `var.region`, `local.name`, `aws_s3_bucket.application.id` | Values are referenced directly: `region`, `name`, `bucket.id` |
| Different block shapes for variables, locals, resources, modules, and outputs | A small, consistent set of declarations and expressions |
| Many interface mistakes surface only during `validate` or `plan` | Unknown names, type mismatches, local module arguments, and provider mappings are checked at compile time with source locations |
| Reuse usually requires a state-visible module boundary or repeated dynamic expressions | Typed components and `static for` remove repetition at compile time without adding state namespaces |
| Object contracts are spread across variable declarations and call sites | Structural types, optional fields, validations, and checked input forwarding define explicit interfaces |
| Refactoring source names can accidentally imply resource-address changes | Source handles and explicit Terraform labels are separate, so readability refactors can preserve state identity |

The result is most noticeable beyond small configurations: less ceremony,
shorter references, reusable typed building blocks, and errors reported against
the source before a plan starts. InfraLang also supports exact compile-time
constants and deterministic expansion without evaluating arbitrary user code,
so generated infrastructure remains predictable and reviewable.

Adoption does not require abandoning existing work. Terraform and OpenTofu
providers remain available, existing Terraform modules are first-class values,
explicit labels can preserve current resource addresses, and `moved`
declarations describe intentional state migrations. Teams can therefore move
one directory at a time while keeping the same backend, state, providers, and
plan/apply workflow.

InfraLang is currently an MVP, so it should be evaluated rather than adopted
for production infrastructure today. Provider schemas are still validated by
Terraform/OpenTofu, and `infralang check` complements rather than replaces
`terraform validate` and plan review.

### Syntax comparison

The following configurations describe the same typed input, provider, S3
bucket, tags, and output.

Terraform HCL:

```hcl
terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

variable "region" {
  type    = string
  default = "eu-central-1"
}

variable "environment" {
  type    = string
  default = "development"

  validation {
    condition     = contains(["development", "staging", "production"], var.environment)
    error_message = "environment must be development, staging, or production."
  }
}

provider "aws" {
  region = var.region
}

locals {
  bucket_name = "application-${var.environment}"
}

resource "aws_s3_bucket" "application" {
  bucket = local.bucket_name

  tags = {
    Name        = local.bucket_name
    Environment = var.environment
  }
}

output "bucket_id" {
  value = aws_s3_bucket.application.id
}
```

InfraLang:

```infra
terraform {
  requiredVersion: ">= 1.5.0",
}

provider AWS from "hashicorp/aws" version "~> 6.0"

input region: string = "eu-central-1"
input environment: string = "development" with {
  validate contains(["development", "staging", "production"], environment)
    else "environment must be development, staging, or production.",
}

configure aws = AWS({ region })

let bucketName = f"application-{environment}"

resource bucket = aws.s3Bucket("application", {
  bucket: bucketName,
  tags: {
    "Name": bucketName,
    "Environment": environment,
  },
})

output bucketId = bucket.id
```

The quoted label `"application"` still produces the Terraform address
`aws_s3_bucket.application`. The source handle `bucket` is only the concise
InfraLang name, so renaming that handle does not rename the managed resource.
Unquoted source fields such as `bucketName` become Terraform-style
`bucket_name` keys when emitted, while quoted map keys keep their exact spelling.

### Larger example: parameterized components

Components package repeated infrastructure structure behind typed parameters
and explicit provider slots. This example creates the same four-resource S3
storage stack for development and production, while allowing each instance to
have a different label, bucket name, versioning policy, and tags:

```infra
terraform {
  requiredVersion: ">= 1.5.0",
}

provider AWS from "hashicorp/aws" version "~> 6.0"

input region: string = "eu-central-1"
input bucketPrefix: string with {
  description: "Globally unique prefix shared by application buckets.",
}

configure aws = AWS({ region })

type StorageConfig = object {
  bucketName: string,
  environment: string,
  versioningEnabled?: bool = true,
  tags?: map<string> = {},
}

const environments = {
  development: {
    label: "app_development",
    suffix: "development",
    versioningEnabled: false,
  },
  production: {
    label: "app_production",
    suffix: "production",
    versioningEnabled: true,
  },
}

component ApplicationStorage(label: string, config: StorageConfig) using {
  aws: AWS,
} {
  let tags = {
    ...config.tags,
    "Environment": config.environment,
    "ManagedBy": "InfraLang",
  }

  resource bucket = aws.s3Bucket(label, {
    bucket: config.bucketName,
    tags: tags,
  })

  resource versioning = aws.s3BucketVersioning(f"{label}_versioning", {
    bucket: bucket.id,
    versioningConfiguration: {
      status: config.versioningEnabled ? "Enabled" : "Suspended",
    },
  })

  resource encryption = aws.s3BucketServerSideEncryptionConfiguration(
    f"{label}_encryption",
    {
      bucket: bucket.id,
      rule: {
        applyServerSideEncryptionByDefault: {
          sseAlgorithm: "AES256",
        },
      },
    },
  )

  resource publicAccess = aws.s3BucketPublicAccessBlock(
    f"{label}_public_access",
    {
      bucket: bucket.id,
      blockPublicAcls: true,
      blockPublicPolicy: true,
      ignorePublicAcls: true,
      restrictPublicBuckets: true,
    },
  )

  export id = bucket.id
  export arn = bucket.arn
}

static for key, environment in environments {
  instantiate storage[key] = ApplicationStorage(
    label: environment.label,
    config: {
      bucketName: f"{bucketPrefix}-{environment.suffix}",
      environment: environment.suffix,
      versioningEnabled: environment.versioningEnabled,
      tags: {
        "Application": "customer-portal",
      },
    },
  ) using {
    aws: aws,
  }
}

output developmentBucketId = storage["development"].id
output productionBucketArn = storage["production"].arn
```

`StorageConfig` checks every component call and supplies defaults for optional
fields. `ApplicationStorage` can use only the provider configurations supplied
through its `using` contract. The `static for` loop expands deterministically
at compile time, while indexed component handles expose typed exports to the
rest of the program. Components and indexes disappear from generated Terraform
JSON; the resulting resources retain explicit addresses such as
`aws_s3_bucket.app_development` and `aws_s3_bucket.app_production`.

## CLI

```shell
bin/infralang check examples/libvirt
bin/infralang build examples/libvirt
bin/infralang build -o generated.tf.json examples/basic/main.infra
bin/infralang version
```

`check` validates a source file or module directory without writing Terraform
JSON. `build` compiles a source file or directory and writes `.tf.json` output;
`-stdout` and `-o` are available for single-file builds.

The `init`, `validate`, `plan`, `apply`, and `destroy` commands build the
InfraLang source in the current directory first and then pass all arguments to
Terraform. Terraform runs in the current directory and keeps its normal stdin,
stdout, stderr, and exit status behavior.

`examples/provider-alias` exercises an external Terraform provider and an
aliased provider configuration. It uses `hashicorp/null` 3.3.1 only as a small
compatibility fixture; new infrastructure should prefer `terraform_data`.

`examples/lvm` demonstrates safe logical-volume management with the local
`github.com/ondrejnov/lvm` provider over SSH. It creates or grows an LV in an
existing volume group and exposes its device path, UUID, and allocated size.

`examples/aws-s3` creates a private, versioned, and encrypted AWS S3 bucket
whose globally unique name includes the current AWS account ID.

Generated `*.tf.json` files are ignored by Git because they are compiler
artifacts.

## Example

```infra
provider Terraform from "terraform.io/builtin/terraform"

input environment: string = "development"
let name = f"application-{environment}"

configure terraform = Terraform({})

resource metadata = terraform.data("metadata", {
  input: {
    name: name,
  },
})

output name = metadata.output.name
```

The compiler maps `terraform.data("metadata", ...)` to the Terraform address
`terraform_data.metadata`. Provider method names and all unquoted object keys
are converted from camelCase source spelling to snake_case wire spelling.

## Language phases

InfraLang's three implemented language phases are cumulative:

1. Phase 1 adds source/wire input aliases, provider shorthand, object punning,
   grouped raw `moved` declarations, resource `when`, and concise input
   validations.
2. Phase 2 adds structural object checking, directory-wide type aliases,
   ordered spreads and conditional fields, typed `each`, checked local module
   interfaces, and `...inputs(value)` forwarding.
3. Phase 3 adds exact constants, deterministic `static for`, compile-time
   labels and indexed handles, canonical type and module imports, and hygienic
   reusable components with virtual exports.

Compile-time declarations erase completely. Terraform JSON contains only
ordinary Terraform settings, providers, variables, locals, resources, data
sources, modules, outputs, and explicit moved items.

```infra
import type { HostConfig } from "./types.infra"
import module HostModule from "./modules/host"

const hosts = { west: { label: "host_west" } }

component Host(label: string, config: HostConfig) using { null: Null } {
  module child = HostModule(label, { ...inputs(config) }) using { null }
  export id = child.id
}

static for key, host in hosts {
  configure providers[key] = Null({})
  instantiate instances[key] = Host(label: host.label, config: host) using {
    null: providers[key],
  }
}

output westId = instances["west"].id
```

`static for` is compile-time expansion. Terraform `forEach` is runtime
collection cardinality and still uses `each`. Indexed providers, modules, and
components are statically selected handles, not Terraform `for_each` objects.

## Source and wire migration

InfraLang source names and Terraform wire names are separate. Top-level input
names automatically become snake_case wire names, while an explicit alias
preserves a deliberate or legacy Terraform variable name:

```infra
input imageId: string
type Config = object { imageId "image_id": string }
```

Unquoted input and object fields use camelCase source members and snake_case wire keys in
every context. Quoted fields preserve the exact wire key and are accessed by
string index. This is a breaking correction from older context-dependent key
conversion. To preserve an existing exact camelCase input wire key, declare an
explicit alias such as `input imageId "imageId": string`. For object literals,
quote the key as `"imageId"` or declare an explicit structural alias such as
`imageId "imageId": string`.

## Boundaries and identity

Immediate `.infra` files form one directory module. Local child modules are
canonicalized, recursively compiled, and interface-checked. Remote modules and
Terraform-only directories remain explicit unchecked boundaries; Terraform
also remains responsible for provider schemas and version-constraint solving.

Type imports may read only canonical relative `.infra` files inside the project
root. Module imports name local directories, registry modules, or URLs; local
InfraLang directories are canonicalized and recursively checked. Constants,
static loops, and components cannot read files or the environment, contact
networks or providers, execute commands, or evaluate Terraform runtime
expressions. Sensitive diagnostics report locations and type contracts without
serializing values.

Terraform identity always comes from explicit resource/data/module labels and
provider aliases. Renaming source symbols, iterators, parameters, or component
handles does not invent a state move or an automatic label. Changing an
explicit label or other address-contributing key requires review and, when the
old state must be retained, an author-supplied `moved` declaration.

## Libvirt example

`examples/libvirt` is a complete InfraLang representation of a larger Libvirt
staging environment, including both local modules. It demonstrates provider
aliases, resource labels, module addresses, optional attribute defaults,
validations, and moved-state declarations. The root uses type imports, exact
constants, static loops, indexed providers, components, virtual exports, and
checked input forwarding while preserving Terraform identity. A directory build
recursively generates Terraform JSON for the root, `libvirt_host`, and
`libvirt_vm` modules.

## Architecture

```text
.infra source
    -> lexer
    -> parser and AST
    -> symbol and type checks
    -> Terraform expression lowering
    -> .tf.json
    -> Terraform/OpenTofu plan and apply
    -> existing provider plugin protocol
```

Terraform remains responsible for unknown values, dependency graph execution,
state locking, provider lifecycle, and apply semantics.

## VS Code

The [`vscode-infralang`](vscode-infralang/) directory contains the desktop VS
Code extension. It provides syntax highlighting, diagnostics, navigation,
completion, hover information, and an InfraLang language server. Packaged VSIX
files are attached to each GitHub release; source-development instructions are
in the extension's README.
