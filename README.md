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
- Input source/wire aliases, object punning, concise validations, grouped raw moves, and conditional resources
- Structural type aliases, ordered object spreads, conditional fields, and checked local module interfaces
- Checked input forwarding with `...inputs(value)` and provider shorthand mappings
- Exact compile-time constants, deterministic static declaration loops, and compile-time labels
- Canonical type-only imports and directory-scoped reusable components
- Indexed provider, module, and component handles that erase before Terraform lowering

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
   labels and indexed handles, canonical `import type`, and hygienic reusable
   components with virtual exports.

Compile-time declarations erase completely. Terraform JSON contains only
ordinary Terraform settings, providers, variables, locals, resources, data
sources, modules, outputs, and explicit moved items.

```infra
import type { HostConfig } from "./types.infra"

const hosts = { west: { label: "host_west" } }

component Host(label: string, config: HostConfig) using { null: Null } {
  module child label from "./modules/host" { ...inputs(config) } using [null]
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

InfraLang source names and Terraform wire names are separate. A legacy
top-level input keeps its exact spelling, while an explicit alias enables
camelCase source code without changing existing Terraform variables:

```infra
input imageId "image_id": string
type Config = object { imageId "image_id": string }
```

Unquoted object fields use camelCase source members and snake_case wire keys in
every context. Quoted fields preserve the exact wire key and are accessed by
string index. This is a breaking correction from older context-dependent key
conversion. To preserve an existing exact camelCase wire key, quote it as
`"imageId"` or declare an explicit structural alias such as
`imageId "imageId": string`.

## Boundaries and identity

Immediate `.infra` files form one directory module. Local child modules are
canonicalized, recursively compiled, and interface-checked. Remote modules and
Terraform-only directories remain explicit unchecked boundaries; Terraform
also remains responsible for provider schemas and version-constraint solving.

Compile-time imports may read only canonical relative `.infra` files inside the
project root. Constants, static loops, and components cannot read files or the
environment, contact networks or providers, execute commands, or evaluate
Terraform runtime expressions. Sensitive diagnostics report locations and type
contracts without serializing values.

Terraform identity always comes from explicit resource/data/module labels and
provider aliases. Renaming source symbols, iterators, parameters, or component
handles does not invent a state move or an automatic label. Changing an
explicit label or other address-contributing key requires review and, when the
old state must be retained, an author-supplied `moved` declaration.

## Staging example

`examples/staging` is a complete InfraLang representation of
`/var/www/infrabot/terraform-staging`, including both local modules. It keeps
the existing provider aliases, resource labels, module addresses, optional
attribute defaults, validations, and moved-state declarations. The root uses
type imports, exact constants, static loops, indexed providers, components,
virtual exports, and checked input forwarding while preserving the original
Terraform identity. A directory build recursively generates Terraform JSON for
the root, `libvirt_host`, and `libvirt_vm` modules.

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

- Provider-schema generated types and completion metadata
- User-defined pure functions
- Formatter and language server
- Native test syntax lowered to Terraform tests
