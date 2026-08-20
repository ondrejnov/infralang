# InfraLang

InfraLang is a programmer-oriented language that compiles to Terraform JSON.
It preserves Terraform and OpenTofu providers, modules, dependency graphs,
state, planning, and apply behavior instead of replacing the ecosystem.

The current release is an MVP compiler. It is suitable for experimenting with
the language and generating Terraform configuration, not yet for production
infrastructure.

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

See [docs/language.md](docs/language.md) for the language reference.

## Build

```shell
mkdir -p bin
go build -o bin/infralang ./cmd/infralang
```

## Usage

```shell
bin/infralang check examples/basic/main.infra
bin/infralang build examples/basic/main.infra
bin/infralang check examples/staging
bin/infralang build examples/staging
terraform -chdir=examples/basic init -backend=false
terraform -chdir=examples/basic validate
```

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

To inspect generated JSON without writing a file:

```shell
bin/infralang build -stdout examples/basic/main.infra
```

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
`terraform_data.metadata`. Provider method names and unquoted provider argument
keys are converted from camelCase to snake_case.

## Staging example

`examples/staging` is a complete InfraLang representation of
`/var/www/infrabot/terraform-staging`, including both local modules. It keeps
the existing provider aliases, resource labels, module addresses, optional
attribute defaults, validations, and moved-state declarations. A directory
build recursively generates Terraform JSON for the root, `libvirt_host`, and
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

## Roadmap

- User-defined pure functions and compile-time constants
- Reusable components above Terraform module boundaries
- Provider-schema generated types and completion metadata
- Imports
- Formatter and language server
- Native test syntax lowered to Terraform tests
