# InfraLang language

InfraLang is a statically checked, expression-oriented frontend for Terraform.
It compiles `.infra` source files to deterministic Terraform JSON and leaves
provider execution, version solving, planning, state, and apply operations to
Terraform or OpenTofu.

The language has three cumulative composition phases:

1. Source ergonomics with explicit Terraform identity.
2. Structural typing and checked local module interfaces.
3. Compile-time metaprogramming and reusable components.

Compile-time declarations are elaborated and erased. They never become custom
Terraform JSON constructs or runtime providers.

## Core declarations

```infra
terraform {
  requiredVersion: ">= 1.5.0",
}

provider AWS from "hashicorp/aws" version "~> 6.0"

input region: string = "eu-central-1"
let name = f"application-{region}"

configure aws = AWS({ region })

resource bucket = aws.s3Bucket("application", {
  bucket: name,
  tags: { "Name": name },
})

data zones = aws.availabilityZones("available", {
  state: "available",
})

output bucketId = bucket.id
```

Top-level declarations may be separated by newlines or semicolons. Commas are
required between function arguments and recommended between object items.

## Source and wire names

InfraLang distinguishes names used in `.infra` expressions from names emitted
on the Terraform wire.

An unaliased top-level input uses its source name in InfraLang and its automatic
snake_case form on the Terraform wire. Use a quoted alias only to preserve an
existing non-standard Terraform variable name or another deliberate wire name:

```infra
input imageId "image_id": string

let selected = imageId
```

The generated Terraform variable is `image_id`; `imageId` is the only source
identifier. The alias is redundant in this example and can be omitted:

```infra
input imageId: string
```

For object literals and structural object fields, an unquoted camelCase key is
the source name and its snake_case form is the wire key in every context:

```infra
type Image = object {
  imageId "image_id": string,
  diskSizeGib: number,
  "ApplicationName": string,
}

let image = {
  imageId: "ami-123",
  diskSizeGib: 20,
  "ApplicationName": "api",
}

output id = image.imageId
output tag = image["ApplicationName"]
```

This emits `image_id`, `disk_size_gib`, and the exact quoted key
`ApplicationName`. A quoted field has no identifier-style source name and is
accessed by string index. Explicit aliases are also available in object type
fields, as shown by `imageId "image_id"`.

Provider method names become Terraform type suffixes using the same camelCase
to snake_case rule: `aws.s3Bucket` becomes `aws_s3_bucket`, and
`lvm.logicalVolume` becomes `lvm_logical_volume`.

### Breaking migration rule

Older InfraLang converted unquoted object keys only in selected provider
argument contexts. The current rule is deliberately context-independent:
unquoted object keys become snake_case wire keys everywhere. This can change an
existing exact camelCase object key.

To preserve an exact camelCase wire key during migration, quote it:

```infra
let payload = { "imageId": value }
```

For a reusable structural contract, preserve it with an explicit alias:

```infra
type Payload = object { imageId "imageId": string }
```

Top-level input names are converted to snake_case by default. Adding or changing
an explicit input alias is a Terraform interface change and must be reviewed
like renaming a Terraform variable. This is a breaking migration for older
files that relied on camelCase top-level Terraform variable names; preserve such
a name explicitly with `input imageId "imageId": string`.

## Phase 1: source ergonomics

### Object punning

An object field with no colon uses the source identifier with the same name:

```infra
let region = "eu-central-1"
configure aws = AWS({ region })
```

The wire key still follows the normal object-key rule.

### Provider mapping shorthand

When the child provider slot and local provider handle have the same name, an
object pun avoids repeating the mapping:

```infra
import module Child from "./modules/child"

module child = Child("child", {
  region,
}) using { aws }
```

This is equivalent to `using { aws: aws }`. The expanded mapping is checked in
the same way as the long form. The inferred array form `using [aws]` is also
accepted when the provider's Terraform local name should determine the slot.

### Concise validation

Inputs accept one or more concise validations inside metadata:

```infra
input hostname: string with {
  description: "VM hostname.",
  validate length(trimspace(hostname)) > 0 else "hostname must not be empty.",
  validate length(hostname) <= 63 else "hostname must contain at most 63 characters.",
}
```

The condition is a Terraform runtime boolean expression. The message must be a
static string. Existing `validations: [{ condition, errorMessage }]` metadata is
also supported.

### Conditional resources

```infra
input enabled: bool = false

resource optional = terraform.data("optional", {
  input: "value",
}) when enabled
```

`when condition` lowers to Terraform `count = condition ? 1 : 0`. It conflicts
with an explicit `count` or `forEach` on the same resource. A conditional
resource is therefore collection-shaped; index it before direct attribute
access, or iterate over it.

### Grouped raw moves

The original string form remains valid:

```infra
moved from "module.old" to "module.host.module.vm[\"api\"]"
```

Multiple exact addresses can be grouped with backtick raw-address literals:

```infra
moved {
  `module.old["api"]` -> `module.host.module.vm["api"]`,
  `null_resource.old` -> `terraform_data.current`,
}
```

Moved addresses are intentionally not resolved as InfraLang symbols because a
source address normally refers to infrastructure no longer declared in the
program.

## Phase 2: structural composition

### Types and aliases

Inputs, constants, component parameters, and object fields support:

- `string`, `number`, `bool`, and `dynamic`
- `list<T>`, `set<T>`, and `map<T>`
- `optional<T>`
- structural `object { ... }`
- named aliases

```infra
export type Machine = object {
  ipAddress "ip_address": string,
  memory?: number = 1024,
  cpuMode "cpu_mode"?: string,
}

type Fleet = map<Machine>
input machines: Fleet
```

`type` is local to the directory module. `export type` additionally permits a
type-only import from another file. Aliases are structural, not nominal: a
value is assignable when its shape and member types satisfy the expanded target
contract. Alias cycles and duplicate source or wire fields are rejected.

Provider nested blocks are inferred from the provider schema rather than from
the InfraLang value type. An object value assigned to a provider field declared
as a nested block is lowered to Terraform JSON as an array containing one
object, so ordinary object syntax remains valid:

```infra
type SshConfig = object { host: string, user: string }
input ssh: SshConfig
configure lvm = Lvm({ ssh })
```

The CLI obtains provider schemas with `terraform providers schema -json` when
compiling a module. Direct compiler callers can provide the same metadata via
`CompileOptions.ProviderSchemas`.

An `optional<T>` input without an explicit default becomes a nullable Terraform
variable with `default = null` and the inner Terraform constraint `T`.

### Ordered spreads and conditional assignments

Object items are evaluated in source order. A later field or spread overrides
an earlier wire key:

```infra
let config = {
  ...defaults,
  memory: 2048,
  qemuGuestAgentEnabled: true when installAgent,
}

if (rootPassword != null) {
  config = {
    ...config,
    password: rootPassword,
    chpasswd: { expire: false },
  }
}
```

`...value` requires an object-compatible value. `field: value when condition`
conditionally contributes that field and requires a boolean condition.

`if (condition) { assignments... }` conditionally updates previously declared
`let` values. The body accepts only assignments; resources, modules, data
sources, and other declarations remain outside the block. An assignment may
refer to its own target, where the reference means the value before that
assignment. Multiple assignments are applied in source order. The compiler
folds the updates into one final Terraform local, so no duplicate local or
self-reference reaches Terraform. There is no `else` form; use a conditional
expression when selecting between two values. For an optional resource, use
the existing `resource ... when condition` form.

Conditional runtime fields and assignments lower through Terraform conditional
and merge semantics. Compile-time false field conditions disappear during
elaboration.

### Typed runtime iteration

Terraform runtime iteration remains available on resources and modules:

```infra
resource vm = libvirt.domain("vm", {
  name: each.key,
  memory: each.value.memory,
}) with {
  forEach: machines,
}
```

For a `map<T>`, `each.key` is `string` and `each.value` is `T`. For a set,
`each.key` and `each.value` use the set element type. `each` is unavailable
outside a declaration with `forEach`.

### Local module interfaces

All immediate `.infra` files in a directory form one InfraLang module. A local
child source is recursively compiled and checked when its canonical directory
contains InfraLang files:

```infra
import module LibvirtVm from "../libvirt_vm"

module vm = LibvirtVm("vm", {
  hostname: each.key,
  commonConfig,
}) using { libvirt, lvm } with {
  forEach: machines,
  dependsOn: [network],
}
```

Checks include required and unknown child inputs, input assignability,
available child outputs, provider slot names and provider source identities,
and cycles between local modules. Optional child inputs may be omitted.

Remote registry modules, remote URLs, and local Terraform-only directories are
explicit unchecked interface boundaries. Their source and provider mapping are
emitted for Terraform to resolve.

`import module Name from "source"` is directory-scoped and compile-time only.
It separates the reusable source from each instance's InfraLang handle,
Terraform label, arguments, providers, and metadata. Duplicate import names and
unknown imported module names are rejected. Imports do not emit Terraform
blocks; only `module handle = Name(label, arguments)` declarations do.

### Checked input forwarding

`...inputs(value)` is valid only inside a module argument object:

```infra
import module Child from "./child"

module child = Child("child", {
  ...inputs(config),
  hostname: overrideName,
})
```

The forwarded value must have a statically known structural object type. Its
source fields are matched to the child input source names, then emitted with the
child's wire names. Later explicit arguments override earlier forwarded fields.
Unknown, missing, duplicate, or incompatible fields are diagnosed. This is not
a general object spread and does not defer interface matching to Terraform.

## Phase 3: compile-time composition

### Exact constants

```infra
const retryCount: number = 2 + 3
const environments = {
  production: { label: "prod" },
  staging: { label: "staging" },
}
```

Constants may reference other constants and static loop bindings. Supported
compile-time values are null, booleans, strings, exact decimal numbers, lists,
and objects. Operators, conditionals, formatted strings, member/index access,
object spreads, conditional fields, and comprehensions are evaluated
without floating-point rounding. A result such as `1 / 3`, which has no finite
decimal JSON representation, is rejected.

Function calls are not allowed in compile-time expressions. Inputs, locals,
resources, data sources, modules, component instances, and Terraform runtime
values cannot be referenced. Constant dependency cycles are diagnosed.

### Deterministic static loops

```infra
import module Child from "./child"

static for key, environment in environments {
  module children[key] = Child(environment.label, {})
}
```

`static for value in list` iterates in list order. The optional key binding is
the zero-based exact numeric index. `static for key, value in object` iterates
by lexically sorted wire key, independent of source map order. Nested static
loops are supported.

The loop body may contain declarations, including provider configurations,
resources, data sources, modules, component instances, and nested static
loops. Every iteration is cloned with provenance so generated identity
collisions identify the iterations that produced them.

This is not Terraform `for_each`:

- `static for` decides declaration cardinality during InfraLang compilation.
- Resource/module `forEach` decides instance cardinality during Terraform
  evaluation and exposes `each`.

### Compile-time labels and indexed handles

Resource/data labels, module labels, and indexed handle keys can be compile-time
expressions:

```infra
import module Child from "registry.example/child"

const regions = {
  east: { label: "east_child" },
  west: { label: "west_child" },
}

static for key, region in regions {
  configure providers[key] = Null({})
  module children[key] = Child(region.label, {}) using {
    "null": providers[key],
  }
}

output westId = children["west"].id
```

Provider indexes must be compile-time strings and become provider aliases.
Module and component indexes may be compile-time strings or exact numbers.
Numerically equal indexes such as `1` and `1.0` are the same key. An indexed
handle lookup must name a declared key.

Indexed handles are static symbol tables. They do not emit Terraform
`for_each`, `count`, index syntax, or synthetic wrapper modules.

### Type-only imports

```infra
import type { CommonConfig, Machine } from "./types.infra"
```

Only aliases declared with `export type` can be imported. Imports are
type-only: imported files cannot contribute runtime values or side effects
through the import. Duplicate imports, missing exports, local/imported name
collisions, and type-import cycles are rejected with the canonical chain.

The import path must be a relative path to a `.infra` file. It is canonicalized
before lookup, must remain inside the project root, and cannot traverse through
symlinks to escape that root. Absolute paths, remote URLs, extension guessing,
and environment-dependent search paths are not supported.

### Components

A component is a typed, directory-scoped declaration template:

```infra
import module LibvirtHost from "./modules/libvirt_host"

component HostDeployment(label: string, config: HostCallConfig) using {
  libvirt: Libvirt,
  lvm: Lvm,
} {
  module host = LibvirtHost(label, {
    ...inputs(config),
  }) using { libvirt, lvm }

  export vms = host.vms
}
```

Instantiate it with typed arguments and provider bindings:

```infra
instantiate hosts["db1"] = HostDeployment(
  label: "host_db1",
  config: db1Config,
) using {
  libvirt: libvirtHosts["db1"],
  lvm: lvmHosts["db1"],
}

output db1 = hosts["db1"].vms["db1"]
```

Arguments are checked by name and structural type. Provider slots are checked
against provider source identities, not only local names. Component exports are
virtual typed handles usable by later expressions; they do not emit Terraform
outputs unless the caller explicitly declares an `output`.

Expansion is hygienic. Component-local declaration handles are renamed to
avoid capture, while explicit Terraform labels and provider aliases are not
silently prefixed or changed. Component parameters, local iterators, and nested
comprehension bindings shadow safely. Direct and indirect component recursion
is rejected.

Component declarations, parameters, instances, and exports erase before
Terraform lowering. Their expanded resources, modules, and provider mappings
are ordinary Terraform JSON entries.

## Runtime expressions

Runtime expressions support literals, arrays, objects, function calls, member
access, indexing, unary operators, arithmetic, comparisons, boolean operators,
null coalescing (`??`), conditional expressions, and list/object
comprehensions:

```infra
let enabled = [for name, machine in machines: name if machine.enabled]
let addresses = {for name, machine in machines: name => machine.ipAddress}
let merged = { ...base, ...overrides }
let document = yamlencode(merged)
```

An interpolated string uses `f"...{expression}..."`. A regular string is
always literal. Terraform function calls such as `merge`, `concat`, and
`yamlencode` remain runtime Terraform expressions and are not available to
`const` evaluation.

## Resource and module metadata

The resource declaration contains a provider handle, provider resource kind,
explicit Terraform label, argument object, and optional `with` clause:

```infra
resource vm = libvirt.domain("vm", {
  name: "worker-1",
}) with {
  dependsOn: [rootImage],
  lifecycle: {
    preventDestroy: true,
    ignoreChanges: [address("devices.consoles[0].source.pty.path")],
  },
}
```

Supported metadata includes `count`, `forEach`, `dependsOn`, `lifecycle`, and
other Terraform resource meta-arguments. `address()` marks a static Terraform
traversal so lifecycle paths cannot be confused with ordinary strings.

Module Terraform meta-arguments are separated with `with`:

```infra
import module Child from "./child"

module child = Child("child", {}) with {
  forEach: machines,
  dependsOn: [network],
}
```

Input and output metadata supports descriptions and sensitivity. Input
declarations also support defaults; input metadata supports nullability and
validation.

## Static and runtime boundary

InfraLang intentionally keeps the phases separate:

| Construct | Evaluation time | May depend on Terraform runtime values | Emitted directly |
| --- | --- | --- | --- |
| `type`, `import type`, `import module` | project loading/checking | no | no |
| `const` | compile time | no | no |
| `static for` | compile time | no | no |
| `component`, `instantiate`, `export` | compile time | no | no |
| indexed provider/module/component handle | compile time | no | no |
| `input`, `let`, runtime expression | Terraform runtime | yes | yes |
| resource/module `forEach` and `each` | Terraform runtime | yes | yes |
| provider/resource/data/module/output | lowering and Terraform runtime | yes | yes |

Compile-time code cannot perform file or environment reads, execute commands,
contact networks, query providers, inspect Terraform state, or call Terraform
functions. Its only external input is canonical project source loaded by the
type-import and imported-local-module resolver. This makes elaboration deterministic and
prevents configuration compilation from becoming a secret-exfiltration or
arbitrary-code-execution surface.

Runtime expressions may describe Terraform operations such as `file()` when
Terraform supports them; InfraLang emits the expression but does not execute it
during compilation.

## Terraform identity and state

Terraform identity comes only from address-contributing declarations:

- provider source plus explicit alias
- resource/data type plus explicit label
- module explicit label
- Terraform runtime `count`/`for_each` keys

InfraLang import names, source handles, constant names, static iterator names,
component names, component parameter names, and component instance handles are
not Terraform identity. Renaming or reordering only those source constructs
must not change generated addresses.

Conversely, changing a resource/data/module label, provider alias, provider
source, module source, static key used as an alias/label, or Terraform runtime
cardinality can change identity. InfraLang does not infer state migrations from
source renames. Review the generated address and author an explicit `moved`
declaration when existing state should follow the new address.

Components are transparent for state identity: expansion does not introduce a
component namespace. This allows repeated source structure to be refactored
into a component while retaining labels such as `host_db1` and `host_db2`.
Duplicate expanded Terraform addresses are rejected rather than silently
renamed.

## Determinism and erasure

Directory files, imported signatures, static object iterations, generated
provider configurations, module artifacts, and JSON keys are ordered
deterministically. Compiling unchanged source repeatedly produces byte-stable
artifacts.

No representation of these constructs may survive into Terraform JSON:

- constants and static loops
- source/wire alias declarations
- named type aliases and type imports
- module imports
- components, instances, parameters, or virtual exports
- indexed handle tables
- compile-time provenance metadata

Their effects appear only as ordinary, fully expanded Terraform constructs.

## Diagnostics and sensitive values

Diagnostics include source locations and, for imported files, component
expansions, and static iterations, relevant provenance. Interface failures
describe source/wire field names and expected types.

Sensitive declarations propagate sensitivity through InfraLang's static
checks. Diagnostics must not serialize sensitive literal values, expanded
objects, component arguments, input defaults, provider configurations, or
Terraform expression payloads. Errors report declaration names, locations, and
type contracts instead.

## Current boundary

InfraLang does not replace Terraform's graph, provider-schema validation,
version solver, state engine, plan, or apply lifecycle. Provider resource
attributes remain dynamically typed until provider-schema type generation is
implemented.

The current language also has no user-defined runtime functions, formatter,
language server, or native Terraform test syntax. Remote module interfaces are
not fetched or inferred during compilation.
