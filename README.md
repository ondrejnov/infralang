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
terraform -chdir=examples/basic init -backend=false
terraform -chdir=examples/basic validate
```

`examples/provider-alias` exercises an external Terraform provider and an
aliased provider configuration. It uses `hashicorp/null` 3.3.1 only as a small
compatibility fixture; new infrastructure should prefer `terraform_data`.

`examples/lvm` demonstrates safe logical-volume management with the local
`github.com/ondrejnov/lvm` provider over SSH. It creates or grows an LV in an
existing volume group and exposes its device path, UUID, and allocated size.

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

`examples/staging/main.infra` is a direct InfraLang representation of the
current `/var/www/infrabot/terraform-staging` root configuration. It keeps the
existing provider aliases and Terraform module labels, including
`module.host_db1` and `module.host_db2`.

The staging example is checked as part of the test suite but is not initialized
inside this repository because its local module source belongs to the sibling
Infrabot project.

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
- Components and statically keyed resource loops
- Provider-schema generated types and completion metadata
- Imports and multi-file packages
- State move declarations
- Formatter and language server
- Native test syntax lowered to Terraform tests
