---
name: infralang
description: Use when creating, editing, reviewing, explaining, compiling, or debugging InfraLang files (`.infra`, `types.infra`) or their generated Terraform JSON (`.tf.json`); when using the `infralang` CLI; when working with InfraLang types, inputs, constants, static loops, components, providers, resources, data sources, modules, outputs, moved state, project loading, or compiler/parser changes. Do not use for ordinary Terraform/OpenTofu HCL unless the task also changes or converts InfraLang source.
---

# InfraLang

Use this skill whenever you create, edit, review, explain, compile, or debug
InfraLang source files with the `.infra` extension.

InfraLang is a statically checked, expression-oriented authoring language for
Terraform and OpenTofu. It compiles `.infra` source to deterministic Terraform
JSON. Terraform or OpenTofu still owns provider schemas, dependency graph
execution, version solving, state, planning, and applying changes.

This skill is intentionally self-contained. It must remain usable when copied
to an agent that cannot access the original InfraLang project. The files in
`reference/` are optional detailed guidance shipped with this skill; never
assume that project files or project examples are available.

## Operating Rules

- Treat `.infra` as the source of truth. Do not hand-edit generated `.tf.json`.
- Before editing a module, inspect every immediate `.infra` file in its directory. All such files form one InfraLang module.
- Inspect local child modules, imported type files, provider mappings, and existing Terraform files that define the boundary around the change.
- Prefer reusable and shared type aliases in a sibling `types.infra` file. Mark exported aliases with `export type` and use explicit `import type { ... } from "./types.infra"` at every consumer; keep a type in the main file only when it is intentionally local to one use.
- Preserve explicit Terraform resource, data, and module labels, provider aliases, module sources, and `moved` addresses unless changing state identity is intentional.
- Prefer the smallest source change that satisfies the request.
- Run `infralang check` after source changes. Build only when generated Terraform JSON is needed.
- Run `terraform validate` or `tofu validate` when provider attributes, remote modules, or Terraform semantics are involved. `infralang check` does not replace it.
- Keep new configuration values in `.infra` `input` declarations and compile-time defaults. Do not create or rely on `.tfvars` files for new configurations unless an existing project workflow explicitly requires external overrides or the user explicitly requests them.
- Do not run `terraform apply` or `tofu apply` without explicit user approval for the real infrastructure change.
- Never put credentials, private keys, tokens, or secret values in `.infra` source or in assistant output.
- Treat `terraform init`, provider installation, plans, state access, and storage examples as environment-affecting operations.

## Mental Model

```text
.infra source
  -> parse and static checks
  -> project loading and compile-time expansion
  -> Terraform expression lowering
  -> deterministic .tf.json
  -> Terraform/OpenTofu validate, plan, and apply
```

There are two important evaluation times:

- Compile time: types, type imports, constants, `static for`, components, and indexed compile-time handles. These constructs are expanded or erased before Terraform JSON is produced.
- Terraform runtime: inputs, locals, resource and data expressions, module instances, outputs, runtime comprehensions, and resource/module `forEach`. These become Terraform configuration and may contain unknown values.

Compile-time code cannot read files or environment variables, execute commands,
contact networks, query providers or state, or call Terraform functions.
Runtime expressions can describe Terraform functions such as `file()`, but the
compiler emits them and does not execute them.

## Terraform View

When reasoning about InfraLang, translate it mentally to ordinary Terraform.
InfraLang changes the authoring syntax and adds static/compile-time checks; it
does not invent a second infrastructure runtime. The compiler emits Terraform
JSON, which has the same meaning as the equivalent HCL below.

| InfraLang                                                 | Terraform concept                             | Typical Terraform address or reference         |
| --------------------------------------------------------- | --------------------------------------------- | ---------------------------------------------- |
| `terraform { ... }`                                       | `terraform` settings and `required_providers` | configuration metadata                         |
| `provider AWS from "hashicorp/aws" ...`                   | an entry in `required_providers`              | provider source identity                       |
| `input region: string`                                    | `variable "region"`                           | `var.region`                                   |
| `let name = expression`                                   | `locals { name = expression }`                | `local.name`                                   |
| `configure aws = AWS({ ... })`                            | `provider "aws" { ... }`                      | provider configuration, not a resource address |
| `data item = aws.method("label", ...)`                    | `data "aws_type" "label" { ... }`             | `data.aws_type.label`                          |
| `resource item = aws.method("label", ...)`                | `resource "aws_type" "label" { ... }`         | `aws_type.label`                               |
| `import module Child from "source"`                       | no emitted block                              | compile-time import only                       |
| `module child = Child("label", ...)`                      | `module "label" { source = "source" version = "..." }` | `module.label`                                 |
| `output value = expression`                               | `output "value" { value = expression }`       | root output                                    |
| `moved ...`                                               | `moved { from = ... to = ... }`               | state migration metadata                       |
| `type`, `const`, `static for`, `component`, `instantiate` | no direct Terraform block                     | expanded or erased before JSON                 |

For example, this InfraLang:

```infra
terraform { requiredVersion: ">= 1.5.0" }
provider AWS from "hashicorp/aws" version "~> 6.0"

input region: string = "eu-central-1"
input environment: string = "development"
configure aws = AWS({ region })

let bucketName = f"application-{environment}"

resource bucket = aws.s3Bucket("application", {
  bucket: bucketName,
  tags: {
    "Name": bucketName,
    "Environment": environment,
  },
})

output bucketArn = bucket.arn
```

is conceptually equivalent to this Terraform HCL:

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
}

provider "aws" {
  region = var.region
}

locals {
  bucketName = "application-${var.environment}"
}

resource "aws_s3_bucket" "application" {
  bucket = local.bucketName

  tags = {
    Name        = local.bucketName
    Environment = var.environment
  }
}

output "bucketArn" {
  value = aws_s3_bucket.application.arn
}
```

The actual compiler output is Terraform JSON, not HCL. The important mapping
is the same: `aws.s3Bucket` becomes the Terraform type `aws_s3_bucket`, the
explicit label `"application"` becomes the state address suffix, and
`bucket.arn` becomes a reference to `aws_s3_bucket.application.arn`. Do not
write Terraform interpolation syntax into normal InfraLang strings; use direct
references or `f"...{expression}..."`.

Terraform metadata follows the same wire mapping: `forEach` becomes `for_each`,
`dependsOn` becomes `depends_on`, and provider/resource argument keys such as
`blockPublicAcls` become `block_public_acls`. Use `each.key` and `each.value`
exactly as you would use Terraform's runtime `each` object.

For a compact declaration-to-JSON and address guide, read
[`reference/terraform-view.md`](reference/terraform-view.md).

## Minimal Syntax

```infra
terraform {
  requiredVersion: ">= 1.5.0",
}

provider AWS from "hashicorp/aws" version "~> 6.0"

input region: string = "eu-central-1"
input environment: string = "development"

configure aws = AWS({ region })

let bucketName = f"application-{environment}"

data account = aws.callerIdentity("current", {})

resource bucket = aws.s3Bucket("application", {
  bucket: bucketName,
  tags: {
    "Name": bucketName,
    "Environment": environment,
  },
}) with {
  lifecycle: { preventDestroy: true },
}

output bucketId = bucket.id with {
  description: "The bucket identifier.",
}
```

The source handle (`bucket`) and the Terraform label (`"application"`) are
different. The emitted resource address is `aws_s3_bucket.application`.
Provider method names and unquoted object keys convert camelCase to snake_case
on the Terraform wire. Quoted object keys preserve their exact spelling.

Supported declarations include:

- `terraform { requiredVersion: "..." }` plus one `backend <type> = { ... }` and at most one `cloud = { ... }` clause
- `provider Name from "namespace/type" version "..."`
- `input name: Type = default with { metadata }`
- `let name = expression`
- `const name = compile_time_expression`
- `configure localName = Provider({ arguments })`
- `data handle = provider.method("label", { arguments })`
- `resource handle = provider.method("label", { arguments }) [with { meta }] [when condition]`
- `import module Name from "source" [version "..."]`
- `module handle = Name("label", { arguments }) [using { providers }] [with { meta }]`
- `output name = expression with { description: "...", sensitive: true }`
- `moved from "old.address" to "new.address"`

Declarations are order-independent. Newlines or semicolons separate top-level
declarations. Function arguments and array items require commas; object items
may use commas or semicolons, with trailing commas recommended in multiline
objects.

### State backend and cloud configuration

The `terraform` block accepts exactly one `backend <type>` clause and at most
one `cloud` clause next to scalar settings such as `requiredVersion`:

```infra
const statePrefix = "application/production"

terraform {
  requiredVersion: ">= 1.5.0",
  backend s3 = {
    bucket: "example-tfstate",
    key: f"{statePrefix}/terraform.tfstate",
    region: "eu-central-1",
    dynamodbTable: "terraform-locks",
    encrypt: true,
  },
  cloud = { organization: "acme" }
}
```

Backend and cloud values must reduce to compile-time constants: literals,
references to `const` values, and constant expressions. Terraform itself
forbids interpolations in backend configuration, so runtime inputs, locals,
resources, and function calls are rejected with diagnostics. Keys follow the
normal object rule: unquoted camelCase keys convert to snake_case wire names,
quoted keys keep their exact spelling. A second `backend` clause, a duplicate
wire key inside one clause, and spreads or non-literal fields are all
diagnosed. The clause lowers to ordinary Terraform JSON —
`"backend": { "s3": { ... } }` or `"cloud": { ... }` inside the `terraform`
object — and never changes resource addresses or state identity.

## Names, Types, and Expressions

Identifiers use ASCII letters, digits, and `_`, and cannot start with a digit.
Strings use double quotes and Go-style escapes. Comments use `#`, `//`, or
`/* ... */`. Use `null`, not the legacy `none` spelling.

Primitive and collection types:

```infra
string
number
bool
dynamic
list<T>
set<T>
map<T>
optional<T>
object {
  requiredField: string,
  optionalField?: number,
  defaultedField?: number = 1024,
}
```

Named aliases are structural rather than nominal:

```infra
export type Machine = object {
  ipAddress "ip_address": string,
  memory?: number = 1024,
}

type Fleet = map<Machine>
input machines: Fleet
```

Compose reusable object contracts with `&`:

```infra
export type VmBase = object {
  bridge: string,
  mtu: number,
}

export type CommonConfig = VmBase & object {
  gateway: string,
  remoteHost: string,
}
```

Each operand must be an `object { ... }` or an alias that resolves directly to
an object. Composition can chain multiple operands, including aliases loaded
with `import type`; scalar, collection, and wrapped types such as
`optional<VmBase>` are invalid operands. Fields are combined from left to right,
but composition never overrides a field: duplicate source names or duplicate
Terraform wire names are rejected, even when their declarations are otherwise
identical. Existing field aliases, optional markers, and defaults are retained.

`&` composes type shapes at compile time; it does not merge runtime object
values. Use object spreads such as `{ ...defaults, ...overrides }` when later
values should replace earlier keys.

Use `export type` only when another `.infra` file needs a type-only import.
Defaults must be compile-time constants. Runtime inputs, locals, resources,
modules, components, and function calls cannot be used in defaults.

Runtime expressions support literals, arrays, objects, member access, indexing,
Terraform function calls, unary `!` and `-`, arithmetic, comparisons, boolean
operators, `??`, conditional expressions, and list/object comprehensions.

```infra
let enabledNames = [for name, machine in machines: name if machine.enabled]
let addresses = {for name, machine in machines: name => machine.ipAddress}
let merged = { ...defaults, ...overrides }
if (password != null) {
  merged = {
    ...merged,
    username,
    password,
  }
}
let displayName = f"service-{environment}"
```

Regular strings are literal. Use `f"...{expression}..."` for interpolation;
use `{{` and `}}` for literal braces inside formatted strings.

Object items are evaluated from left to right. Later fields or spreads override
earlier wire keys. `{ region }` is shorthand for `{ region: region }`.

## Providers, Resources, and Data Sources

`provider` declares the provider source and version constraint. `configure`
creates a provider configuration handle:

```infra
configure aws = AWS({ region })
configure awsEast = AWS("east", { region: "us-east-1" })
```

`configure childSlot = Provider` declares an inherited provider slot inside a
child module and emits no provider block there. Resources and data sources use
configured handles, not provider declaration names.

```infra
resource vm = libvirt.domain("worker", {
  name: "worker-1",
  memory: 2048,
}) with {
  dependsOn: [network],
  lifecycle: {
    preventDestroy: true,
    ignoreChanges: [address("devices.consoles[0].source.pty.path")],
  },
}
```

Resource metadata can contain `count`, `forEach`, `dependsOn`, `lifecycle`, and
other Terraform resource meta-arguments. Data sources do not accept resource
meta-arguments. `address("literal.traversal")` marks a static Terraform
traversal, especially for `lifecycle.ignoreChanges`.

Conditional resources use `when`:

```infra
resource optional = terraform.data("optional", {
  input: "value",
}) when enabled
```

`when` lowers to `count = condition ? 1 : 0`; it conflicts with explicit
`count` and `forEach`. The result is collection-shaped: direct attribute
access `optional.output` is implicitly unwrapped to
`one(terraform_data.optional[*].output)` and yields `null` while the condition
is false. Indexing with `[0]`, iteration, and bare references keep collection
behavior; prefer explicit indexing inside static traversal metadata such as
`lifecycle.ignoreChanges`.

Provider resource/data kind names use snake_case lowering, so
`aws.s3Bucket(...)` becomes `aws_s3_bucket` and
`aws.availabilityZones(...)` becomes `aws_availability_zones`.

## Modules and Components

Use a module for a separate Terraform unit with its own directory, interface,
and Terraform state namespace:

```infra
import module Child from "./child"

module child = Child("child", {
  region,
}) using {
  aws: aws,
} with {
  dependsOn: [network],
}
```

The import emits nothing. The instance remains a Terraform module and the
explicit label contributes `module.child` to Terraform addresses. `using [aws]`
or `using { aws }` is shorthand when the child provider slot and parent handle
have the same name.

A remote source accepts an optional version constraint emitted as the module
block's `version` setting:

```infra
import module Vpc from "terraform-aws-modules/vpc/aws" version "~> 5.0"
```

Local sources beginning with `./`, `../`, or `/` must not declare a version,
the constraint must be a non-empty string, and all imports of one remote source
must use the same constraint (an unversioned reference conflicts with a pinned
one). Terraform resolves one version per source.

Local InfraLang child modules are checked for required and unknown inputs,
structural input assignability, available outputs, provider slot names/source
identities, and cycles. Remote modules and Terraform-only directories remain
explicit unchecked boundaries for Terraform to resolve.

Forward a statically known object to a local module with `...inputs(value)`:

```infra
module child = Child("child", {
  ...inputs(config),
  hostname: overrideName,
})
```

This construct is valid only in a module argument object. Child input wire
names are checked; later explicit arguments override forwarded fields. It is
not a general object spread.

Use a component for typed compile-time reuse without a Terraform module
boundary:

```infra
component Server(label: string, hostname: string) using {
  aws: AWS,
} {
  resource instance = aws.instance(label, {
    hostname: hostname,
  })

  export id = instance.id
}

instantiate server = Server(
  label: "production",
  hostname: "server-01",
) using {
  aws: aws,
}

output serverId = server.id
```

Components are expanded before Terraform lowering. They create no module
namespace, state boundary, or automatic outputs. Component arguments, exports,
and provider slots are checked statically. Component expansion must not create
duplicate Terraform addresses.

## Compile-Time Composition

Constants accept null, booleans, strings, exact decimal numbers, lists, and
objects. They can use other constants and static-loop bindings, but not runtime
values or function calls.

```infra
const environments = {
  staging: { label: "app_staging" },
  production: { label: "app_production" },
}

static for key, environment in environments {
  configure providers[key] = Null({})
  resource marker = providers[key].resource(environment.label, {})
}
```

`static for` determines declaration count during compilation. Lists preserve
order; object iteration uses sorted wire keys. This differs from resource or
module `forEach`, which is evaluated by Terraform and exposes `each.key` and
`each.value`.

Indexed provider, module, and component handles are compile-time symbol tables.
They do not emit Terraform `for_each`, `count`, index syntax, or wrapper
modules. Provider index keys must be compile-time strings and become aliases;
module/component keys may be compile-time strings or exact numbers.

Type-only imports are explicit relative paths to `.infra` files:

```infra
import type { CommonConfig, Machine as HostMachine } from "./types.infra"
```

Only `export type` aliases can be imported. Type imports add no runtime values
or side effects. Paths must remain inside the project root after canonicalizing
relative paths and symlinks.

## Wire Names and State Identity

InfraLang source names are not always Terraform names:

```infra
input imageId: string

let image = {
  imageId: imageId,
  "ApplicationName": "api",
}

output selected = image.imageId
output tag = image["ApplicationName"]
```

The default wire names are `image_id` and `application_name`; the quoted key
stays exactly `ApplicationName`. Use explicit aliases when an existing
Terraform interface must retain a deliberate name:

```infra
input imageId "imageId": string
type Image = object { imageId "imageId": string }
```

Terraform identity comes from provider source plus alias, resource/data type
plus explicit label, module explicit label, and runtime `count`/`for_each` keys.
Source handles, import names, constants, iterator names, component names,
parameters, and component instance handles are not state identity.

If an intentional change alters an address, add an explicit `moved` declaration:

```infra
moved from "module.old" to "module.current"

moved {
  `null_resource.old` -> `terraform_data.current`,
  `module.old["api"]` -> `module.current["api"]`,
}
```

Moved addresses are raw Terraform addresses. Do not interpolate them or resolve
them as current InfraLang symbols.

## CLI Workflow

Use the installed `infralang` executable. If it is not available, obtain a
release binary or build the CLI according to the environment; do not assume a
particular installation layout.

```shell
infralang version
infralang check path/to/main.infra
infralang check path/to/module-directory
infralang fmt path/to/main.infra
infralang build path/to/main.infra
infralang build path/to/module-directory
infralang build -stdout path/to/main.infra
infralang build -o generated.tf.json path/to/main.infra
```

For a single file, `build` writes a sibling `.tf.json` by default. For a module
directory, it writes `main.tf.json` for each compiled module. `-stdout` and
`-o` apply only to single-file builds and are mutually exclusive. Put flags
before the source path. `fmt` atomically formats one valid `.infra` file in
place and prints the filename when the file changed.

The commands `infralang init`, `infralang validate`, `infralang plan`,
`infralang output`, `infralang apply`, and `infralang destroy` compile the
current directory first, then delegate to Terraform with the remaining
arguments. Prefer `check` for a safe compiler-only validation and obtain
approval before `apply` or `destroy`.

Recommended verification sequence:

1. `infralang check SOURCE_OR_MODULE_DIR`
2. `infralang build -stdout FILE.infra` for a single-file output review, or build the directory when artifacts are required.
3. `terraform validate` or `tofu validate` in the generated configuration directory.
4. Review the plan and all address changes before any apply.

Diagnostics include filename, line, column, source context, and a caret. Parse
errors stop compilation. Any diagnostic prevents artifact emission.

## Common Mistakes

- Using a Terraform block or HCL assignment instead of InfraLang declarations.
- Referencing provider declaration `AWS` as a resource handle instead of configuring and using `aws`.
- Expecting `infralang check` to validate every provider attribute or remote-module input.
- Putting `with` metadata on a data source.
- Using a runtime input, Terraform function, or resource value in a `const` or default.
- Using `each` outside a resource or module with runtime `forEach`.
- Treating `static for` as Terraform `for_each`.
- Using `...inputs(value)` as a normal object spread.
- Forgetting that unquoted keys and provider methods become snake_case.
- Changing explicit labels or aliases during a readability refactor without reviewing state addresses.
- Assuming components create a Terraform module namespace or outputs.
- Assuming a module import alone emits a Terraform module block.
- Generating or editing JSON before the source passes `infralang check`.

For focused guidance, read the bundled references:

- [`reference/syntax.md`](reference/syntax.md): syntax and type cheat sheet.
- [`reference/patterns.md`](reference/patterns.md): complete standalone patterns for providers, modules, components, iteration, and moves.
- [`reference/terraform-view.md`](reference/terraform-view.md): InfraLang to Terraform HCL/JSON and address mapping.
- [`reference/troubleshooting.md`](reference/troubleshooting.md): diagnosis and verification checklist.
