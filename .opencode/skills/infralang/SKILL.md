---
name: infralang
description: InfraLang and .infra files: create, explain, review, debug, compile, and validate InfraLang infrastructure code and its Terraform JSON output. Use for InfraLang syntax, types, constants, static loops, components, providers, resources, data sources, modules, inputs, outputs, moved state, or changes to the lexer, parser, compiler, project loader, or CLI.
---

# InfraLang

Use this skill for InfraLang source and for the Go compiler that lowers `.infra` files to deterministic Terraform JSON. InfraLang is a statically checked frontend for Terraform/OpenTofu. It does not replace their provider schemas, graph, version solver, state, plan, or apply behavior.

## Source Of Truth

Resolve ambiguity in this order:

1. `internal/syntax/lexer.go`, `internal/syntax/parser.go`, `internal/syntax/ast.go`, and `internal/syntax/token.go` for accepted syntax.
2. `internal/compiler/` for preparation, validation, component expansion, interfaces, and Terraform lowering.
3. `internal/project/` for type imports, local-module graphs, directory builds, and path security.
4. Tests in `internal/syntax/`, `internal/compiler/`, `internal/project/`, and `cmd/infralang/` for executable behavior.
5. `docs/language.md` and `README.md` for the public contract.
6. `examples/` for established usage patterns.

Do not treat generated `*.tf.json` files as authoritative. They are ignored artifacts and may be stale.

Before editing `.infra` code:

- Read all immediate `.infra` files in the target directory because they form one module.
- Read relevant callers, local child modules, imported type files, and component definitions.
- Preserve explicit Terraform labels, provider aliases, module sources, and `moved` addresses unless the task intentionally changes state identity.
- Reuse nearby source/wire naming, metadata, and provider-passing style.
- Prefer the smallest valid change. Do not invent syntax from Terraform HCL or another language.

## Mental Model

InfraLang has three cumulative composition phases: source ergonomics, structural typing with checked local-module interfaces, and compile-time composition with reusable components.

```text
.infra files
  -> lexing and parsing
  -> project loading and compile-time preparation
     (type imports, constants, static loops, labels, indexed handles, components)
  -> symbol, structural-type, and local-module-interface checks
  -> Terraform expression lowering
  -> deterministic .tf.json
  -> Terraform/OpenTofu validate, plan, and apply
```

Compile-time constructs are elaborated and erased. They never become custom Terraform blocks or runtime providers.

InfraLang checks structural object members and local InfraLang module inputs, outputs, provider slots, and cycles when their shape is known. Provider schemas, remote-module interfaces, Terraform-only child directories, arbitrary provider attributes, and arbitrary Terraform function names remain Terraform's responsibility. `infralang check` does not replace `terraform validate`.

## Core Declarations

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
  }
}

provider AWS from "hashicorp/aws" version "~> 6.0"

input region: string = "eu-central-1" with {
  description: "AWS region.",
}

let name = f"application-{region}"

configure aws = AWS({ region })
configure awsEast = AWS("east", { region: "us-east-1" })
import module Child from "./modules/child"

data zones = aws.availabilityZones("available", {
  state: "available",
})

resource bucket = aws.s3Bucket("application", {
  bucket: name,
  tags: { "Name": name },
}) with {
  dependsOn: [],
  lifecycle: { preventDestroy: true },
}

module child = Child("child", {
  region,
}) using {
  aws: awsEast,
} with {
  dependsOn: [bucket],
}

output bucketId = bucket.id with {
  description: "Created bucket ID.",
}

moved from "module.old" to "module.child"
```

Declaration rules:

- `terraform` supports `requiredVersion` or `required_version`, exactly one `backend <type> = { ... }` clause, and at most one `cloud = { ... }` clause; only one block is allowed.
- `provider` source and version are literal strings.
- `configure name = Provider({...})` creates the default provider configuration.
- `configure name = Provider("alias", {...})` creates an aliased configuration.
- `configure name = Provider` declares an inherited provider slot in a child module and emits no provider block.
- Resources and data sources use configured provider handles, not provider declaration names.
- A resource accepts an optional third object for Terraform meta-arguments. A data source does not.
- `import module Name from "source"` creates a directory-scoped module constructor and emits nothing by itself. A remote source accepts an optional `version "<constraint>"` clause emitted as the module block's `version`; local `./`, `../`, or `/` sources must not declare one, the constraint must be non-empty, and all imports of one source must agree on it.
- A module instance has an InfraLang handle and a separate Terraform label: `module sourceName = Name(labelExpression, arguments)`.
- Module `using` maps child provider slot names to parent configuration handles. Array shorthand such as `using [aws]` infers each child slot from the configuration's provider local name; it equals `using { aws: aws }` only when that inferred name and the handle are both `aws`.
- Module `with` holds Terraform meta-arguments such as `forEach` and `dependsOn`.
- Declarations are order-independent and may be separated by whitespace or semicolons.

## Lexical Rules

- Identifiers start with an ASCII letter or `_`, followed by ASCII letters, digits, or `_`.
- Strings use double quotes and Go-style escapes. Backticks are reserved for raw Terraform addresses in grouped `moved` declarations.
- Comments use `#`, `//`, or `/* ... */`.
- Function arguments and array items require commas when multiple values are present.
- Object items may be comma- or semicolon-separated; prefer trailing commas in multiline objects.
- Literals are strings, exact decimal numbers, `true`, `false`, and `null`. The lexer still accepts legacy `none` as null, but use `null` in new code.

## Source And Wire Names

InfraLang distinguishes source identifiers used in expressions from names emitted to Terraform.

An unaliased top-level input preserves its source identifier in expressions and automatically converts it to snake_case on the Terraform wire. Use a quoted alias only when the wire name must be deliberate or legacy:

```infra
input imageId "image_id": string
let selected = imageId
```

The source identifier is `imageId`; the Terraform variable is `image_id`. The alias is optional here, so `input imageId: string` is equivalent. Adding or changing an input wire name changes the Terraform interface.

In every object context, an unquoted key has an identifier-style source name and a snake_case wire name. Quoted keys preserve their exact wire spelling and are accessed by string index. Structural object fields may declare an explicit wire alias:

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

This rule applies recursively to locals, outputs, defaults, metadata, provider arguments, resource arguments, module arguments, and structural types. Quote arbitrary map keys when spelling or case must survive. Avoid duplicate wire keys after conversion.

Provider method names use the same conversion: `aws.s3Bucket` becomes `aws_s3_bucket`, and `lvm.logicalVolume` becomes `lvm_logical_volume`.

## Types And Metadata

Inputs, constants, component parameters, and object fields support:

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

Use `dynamic` when no stronger type is available. Do not use the implementation alias `any` in new public examples.

Named aliases are structural, not nominal:

```infra
export type Machine = object {
  ipAddress "ip_address": string,
  memory?: number = 1024,
}

type Fleet = map<Machine>
input machines: Fleet
```

Object aliases can compose shared structural contracts. Every operand must
resolve directly to an object (without wrappers such as `optional`), and
duplicate source or wire fields are rejected:

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

`type` is local to the directory module. `export type` can be imported with `import type`. Alias cycles and duplicate source or wire fields are rejected.

Input defaults and optional object-field defaults must reduce to compile-time constants. References to `const` values are allowed; runtime inputs, locals, resources, modules, component instances, Terraform functions, and other runtime values are not. Object-field defaults require `?`.

A direct top-level `optional<T>` input without an explicit default emits Terraform type `T` and `default = null`. It is nullable input behavior, not a general union type.

Input metadata supports `description`, `sensitive`, `nullable`, and validations. Output metadata supports `description` and `sensitive`.

```infra
input hostname: string with {
  description: "VM hostname.",
  validate length(trimspace(hostname)) > 0 else "hostname must not be empty.",
  validations: [{
    condition: length(hostname) <= 63,
    errorMessage: "hostname must contain at most 63 characters.",
  }],
}
```

Concise validation messages must be static strings. Multiple concise and legacy validations preserve source order.

## Objects And Expressions

Runtime expressions support:

- Strings, numbers, booleans, `null`, arrays, and objects.
- Member access and indexing: `vm.id`, `vms[hostname]`.
- Terraform function calls such as `merge`, `concat`, `yamlencode`, and `length`.
- Unary `!` and `-`.
- Arithmetic, comparison, equality, and boolean operators.
- Null coalescing with `??`.
- Conditional expressions: `condition ? whenTrue : whenFalse`.
- List and object comprehensions.
- Formatted strings prefixed by `f`.

```infra
let enabledNames = [for name, machine in machines: name if machine.enabled]
let addresses = {for name, machine in machines: name => machine.ipAddress}
let displayName = f"service-{environment}"
```

Regular strings are literal. Use `f"...{expression}..."` for interpolation and `{{` or `}}` for literal braces.

Object items are evaluated in source order. Later fields and spreads override earlier wire keys:

```infra
let config = {
  ...defaults,
  region,
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

- `{ region }` is object punning for `{ region: region }`.
- `...value` requires an object-compatible value.
- `field: value when condition` conditionally contributes a field and requires a boolean condition.
- `if (condition) { name = value }` conditionally updates a previously declared `let`; only assignments are accepted and target references mean the previous value.
- Resource, module, data, and other declarations are not accepted in `if`; use resource `when` for optional resources.
- Compile-time false fields disappear during preparation; runtime conditions lower through Terraform merge semantics.

Operator precedence from lowest to highest is conditional, `??`, `||`, `&&`, equality, ordering, `+`/`-`, `*`/`/`/`%`, unary, then member/index/call postfix operations.

`address("...")` marks a static Terraform traversal wherever the expression is rendered; its intended use is traversal metadata such as `lifecycle.ignoreChanges`. Use exactly one literal-string argument. The compiler recognizes that specific shape; otherwise a call named `address` follows ordinary expression lowering.

## Resources And Runtime Iteration

Resource `with` clauses include `count`, `forEach`, `dependsOn`, `lifecycle`, and other Terraform resource meta-arguments:

```infra
resource vm = libvirt.domain("vm", {
  name: each.key,
  memory: each.value.memory,
}) with {
  forEach: machines,
  dependsOn: [network],
  lifecycle: {
    ignoreChanges: [address("devices.consoles[0].source.pty.path")],
  },
}
```

The compiler provides typed `each.key` and `each.value` inside both resource and module declarations with `forEach`. For `map<T>`, the key is `string` and the value is `T`. Runtime `forEach` accepts `set<string>`, where both fields are `string`; other set element types are rejected. `each` is unavailable outside such a declaration and while evaluating its own `forEach` expression. `count.index` scope is not currently installed.

A conditional resource uses `when`:

```infra
resource optional = terraform.data("optional", {
  input: "value",
}) when enabled
```

`when condition` lowers to `count = condition ? 1 : 0`, conflicts with explicit `count` or `forEach`, and makes the resource collection-shaped. Direct attribute access `res.attr` is implicitly unwrapped to `one(res[*].attr)` and yields `null` while the condition is false; indexing with `[0]`, iteration, and bare references keep collection behavior. Prefer explicit indexing inside static traversal metadata such as `lifecycle.ignoreChanges`.

## Local Modules

All immediate `.infra` files in one directory form one InfraLang module. Local child sources beginning with `.` are recursively loaded when their canonical directory contains `.infra` files.

Checked local interfaces include:

- Required, optional, and unknown child inputs.
- Input structural assignability and child wire names.
- Available child outputs.
- Provider slot names and provider source identities.
- Cycles between local modules.

Remote registry modules, remote URLs, and local Terraform-only directories are unchecked interface boundaries. Terraform resolves those sources and provider mappings.

Use `...inputs(value)` only inside a module argument object to forward a statically known structural object:

```infra
import module Child from "./child"

module child = Child("child", {
  ...inputs(config),
  hostname: overrideName,
}) using { libvirt, lvm }
```

The current compiler matches forwarded object wire keys to child input wire names. Use explicit source/wire aliases when parent field spelling and a legacy child input name would otherwise lower differently. Later explicit arguments override earlier forwarded fields. Unknown, missing, duplicate, and incompatible fields are diagnosed. This is not a general object spread.

Directory builds emit `main.tf.json` for all modules only after project-wide compilation succeeds. Diagnostics produce no partial artifacts. A child directory with only Terraform `.tf` files remains Terraform's responsibility.

## Compile-Time Composition

### Constants

```infra
const retryCount: number = 2 + 3
const environments = {
  production: { label: "prod" },
  staging: { label: "staging" },
}
```

Constants may reference other constants and static loop bindings. Values include null, booleans, strings, exact decimal numbers, lists, and objects. Operators, conditionals, formatted strings, member/index access, spreads, conditional fields, and comprehensions are evaluated without floating-point rounding. Non-finite decimal JSON results such as `1 / 3` are rejected.

Function calls and runtime declarations are unavailable during constant evaluation. Dependency cycles are diagnosed.

### Static Loops And Indexed Handles

The following fragment assumes the provider declaration used by the surrounding project:

```infra
import module Child from "./child"

static for key, environment in environments {
  configure providers[key] = Null({})
  module children[key] = Child(environment.label, {}) using {
    "null": providers[key],
  }
}

output stagingId = children["staging"].id
```

`static for value in list` preserves list order and optionally exposes a zero-based exact numeric index. Object iteration uses lexically sorted wire keys. Nested loops are supported.

Static loops determine declaration cardinality during InfraLang compilation. Resource/module `forEach` determines instance cardinality during Terraform evaluation and exposes `each`.

Resource/data labels, module labels, and indexed-handle keys may use compile-time expressions. Labels must resolve to non-empty valid Terraform identifier strings; runtime values are rejected. Provider indexes must be compile-time strings and become provider aliases. Module and component indexes may be compile-time strings or exact numbers. Numerically equal indexes such as `1` and `1.0` are the same key.

Indexed provider, module, and component handles are static symbol tables. They do not emit Terraform `for_each`, `count`, index syntax, or wrapper modules.

### Type-Only Imports

```infra
import type { CommonConfig, Machine as HostMachine } from "./types.infra"
```

Only `export type` aliases can be imported. Type imports add no runtime values or side effects. Paths must be relative `.infra` files, canonicalize inside the project root, and cannot escape through `..` or symlinks. Absolute paths, remote URLs, extension guessing, environment-dependent searches, duplicate imports, missing exports, name collisions, and cycles are rejected.

Type imports require project/directory compilation; standalone compilation cannot resolve them.

### Components

A component is a typed, directory-scoped declaration template. This fragment assumes the referenced types and providers exist in the surrounding directory project:

```infra
import module LibvirtHost from "./modules/libvirt_host"

component HostDeployment(label: string, config: HostConfig) using {
  libvirt: Libvirt,
  lvm: Lvm,
} {
  module host = LibvirtHost(label, {
    ...inputs(config),
  }) using { libvirt, lvm }

  export vms = host.vms
}

instantiate hosts["db1"] = HostDeployment(
  label: "host_db1",
  config: db1Config,
) using {
  libvirt: libvirtHosts["db1"],
  lvm: lvmHosts["db1"],
}

output db1 = hosts["db1"].vms["db1"]
```

Arguments are checked by name and structural type. Provider slots are checked by provider source identity. Component exports are virtual typed handles; they emit no Terraform outputs unless the caller declares an `output`.

Expansion is hygienic for source handles and bindings, but explicit Terraform labels and provider aliases are not silently prefixed. Components add no state namespace. Duplicate expanded Terraform addresses and direct or indirect component recursion are rejected.

## Terraform Lowering And Identity

| InfraLang declaration | Terraform reference |
| --- | --- |
| `input source "wire_name"` | `var.wire_name` |
| `let name` | `local.name` |
| `resource item = aws.s3Bucket("label", ...)` | `aws_s3_bucket.label` |
| `data item = aws.availabilityZones("label", ...)` | `data.aws_availability_zones.label` |
| `module child = Child("stable", ...)` | `module.stable` |

Constant values that reach ordinary declarations become native JSON. Runtime expressions become Terraform JSON interpolation expressions. Do not manually write Terraform interpolation syntax in ordinary InfraLang strings.

Terraform resource and module state addresses come from resource/data type plus label, module label, and runtime `count`/`for_each` keys. Provider configurations have separate identity from provider source plus alias and are recorded in state, but the source is not textually part of a resource address. Module import names, source handles, constants, iterator names, component names, parameters, and component instance handles do not create Terraform identity.

Changing an explicit label, provider alias, static key used as a label/alias, or runtime cardinality can change state addresses. Changing a provider source rebinds provider configuration identity without textually renaming resource addresses. Changing a module source does not rename the `module.<label>` call itself, but can replace the infrastructure and nested addresses behind that call. InfraLang does not infer migrations from source renames. Review generated addresses and add explicit `moved` declarations when state should follow an intentional change.

Both moved forms are supported:

```infra
moved from "module.old" to "module.current"

moved {
  `module.old["api"]` -> `module.current["api"]`,
  `null_resource.old` -> `terraform_data.current`,
}
```

Raw moved addresses are not interpolated or resolved as InfraLang symbols.

Constants, static loops, aliases, imports, components, instances, virtual exports, indexed-handle tables, and provenance never survive into Terraform JSON. Recompiling unchanged source produces byte-stable artifacts.

## CLI Workflow

Build the current compiler before relying on local results:

```shell
mkdir -p bin
go build -o bin/infralang ./cmd/infralang
```

```shell
bin/infralang check SOURCE.infra
bin/infralang check MODULE_DIR
bin/infralang fmt SOURCE.infra
bin/infralang build SOURCE.infra
bin/infralang build MODULE_DIR
bin/infralang build -stdout SOURCE.infra
bin/infralang build -o OUTPUT.tf.json SOURCE.infra
bin/infralang init|validate|plan|output|apply|destroy [TERRAFORM_ARGS...]
bin/infralang version
bin/infralang help
```

Place build flags before the source path. `-o` and `-stdout` are mutually exclusive and apply only to single-file `.infra` builds. `-h`, `--help`, `-version`, and `--version` are accepted aliases.

`fmt` atomically formats one valid `.infra` file in place; it prints the filename when the file changed. `init`, `validate`, `plan`, `output`, `apply`, and `destroy` first compile the InfraLang source in the current directory, then delegate to Terraform with all remaining arguments and its normal stdin, stdout, stderr, and exit status. Never run `apply` or `destroy` without explicit user approval.

When compiling a directory or module project, the CLI also runs `terraform providers schema -json` (errors are ignored if Terraform is unavailable) so provider nested blocks can be recognized and lowered as arrays. Provider resource/data arguments stay dynamically typed.

Diagnostics are sorted and printed with file, line, column, message, source-line context, and a caret. Internal diagnostic codes are not printed by the CLI. Parse errors stop compiler execution, and any diagnostic prevents artifact emission.

For `.infra` edits:

1. Run `bin/infralang check PATH`; it compiles without writing generated files.
2. For one file, inspect exact output with `bin/infralang build -stdout FILE.infra`.
3. For imports and checked local modules, validate the directory/project instead of an isolated file.
4. Build a directory only when generated artifacts are needed.
5. Run `terraform validate` or `tofu validate` when provider or remote-module semantics matter and tooling is available.

For lexer, parser, compiler, project loader, or CLI changes:

```shell
go test ./...
go vet ./...
go build -o bin/infralang ./cmd/infralang
bin/infralang check RELEVANT_PATH
```

Add focused tests for syntax, diagnostics, compile-time preparation, structural types, local interfaces, component hygiene, state identity, CLI behavior, and exact Terraform JSON lowering. Run Terraform validation integration tests when relevant.

## Safety And Pitfalls

- Never run `terraform apply` or `tofu apply` unless the user explicitly requests and approves real infrastructure changes.
- Treat `init`, plan operations, provider initialization, and storage examples as environment-affecting. The LVM example can manage real logical volumes.
- Do not edit state, `.terraform/`, lock files, or ignored generated JSON unless explicitly required.
- Never expose provider credentials, authorization headers, sensitive variable values, state content, or secrets from generated artifacts.
- Do not place secrets in source. Compiler type errors redact sensitive values, but CLI diagnostics echo the offending source line.
- Do not infer that `infralang check` validates provider field names, remote-module interfaces, or arbitrary Terraform functions.
- Do not put resource meta-arguments on `data` declarations.
- Defaults may use compile-time constants, but never runtime references or function calls.
- Preserve explicit resource, data, and module labels unless state identity should change.
- Use `moved` when an intentional address change must preserve state.
- Quote arbitrary map keys; all unquoted input and object keys are recursively converted to snake_case wire keys.
- Compile-time evaluator and control expressions cannot read files or environment variables, execute commands, access networks, query providers or state, or call Terraform functions. Ordinary runtime expressions inside components and expanded declarations may describe Terraform functions such as `file()`, but InfraLang does not execute them.
- Keep unsupported roadmap syntax out of `.infra`: there are no user-defined runtime functions, formatter directives, language server features, native Terraform tests, or provider-schema-generated types.

## Canonical References

- Public language contract: `docs/language.md`
- Project overview and CLI examples: `README.md`
- Lexer, parser, AST, tokens: `internal/syntax/`
- Core compiler and lowering: `internal/compiler/compiler.go`
- Compile-time preparation: `internal/compiler/elaborate.go`, `internal/compiler/elaborate_tree.go`
- Components and indexed instances: `internal/compiler/components.go`
- Structural types and local interfaces: `internal/compiler/types.go`, `internal/compiler/interfaces.go`
- Project graph and imports: `internal/project/graph.go`, `internal/project/imports.go`, `internal/project/project.go`
- CLI and directory artifact writing: `cmd/infralang/main.go`
- Formatter used by `fmt`: `internal/formatter/formatter.go`
- Components vs. modules guide: `docs/component_vs_module.md`; checked input forwarding: `docs/input-forwarding.md`
- End-to-end typed component example: `examples/libvirt/`
- Small language example: `examples/basic/main.infra`
- Provider alias example: `examples/provider-alias/`
