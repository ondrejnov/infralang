---
name: infralang
description: InfraLang and .infra files: create, explain, review, debug, compile, and validate InfraLang infrastructure code and its Terraform JSON output. Use when a task mentions InfraLang, .infra syntax, providers, resources, data sources, modules, inputs, outputs, moved state, or changes to the InfraLang lexer, parser, compiler, or CLI.
---

# InfraLang

Use this skill to work with InfraLang source and the compiler that lowers it to Terraform JSON. InfraLang is an MVP frontend for Terraform/OpenTofu, not a replacement for their graph, state, provider, plan, or apply behavior.

## Source Of Truth

Resolve ambiguity in this order:

1. `internal/syntax/lexer.go`, `internal/syntax/parser.go`, and `internal/syntax/token.go` for accepted syntax.
2. `internal/compiler/compiler.go` for validation and Terraform lowering.
3. Tests in `internal/syntax/`, `internal/compiler/`, and `cmd/infralang/` for executable behavior.
4. `docs/language.md` and `README.md` for the public contract.
5. `examples/` for established usage patterns.

Do not treat generated `*.tf.json` files as authoritative. They are ignored artifacts and may be stale.

Before editing `.infra` code:

- Read the complete target module and its callers or child modules.
- Preserve quoted Terraform labels and `moved` addresses unless the task explicitly changes state addresses.
- Reuse the naming, metadata, and provider-passing style already present nearby.
- Prefer the smallest valid change; do not introduce imagined syntax from Terraform HCL or another language.

## Mental Model

The pipeline is:

```text
.infra source
  -> lexer and parser
  -> symbol and basic type checks
  -> Terraform expression lowering
  -> .tf.json
  -> Terraform/OpenTofu validate, plan, and apply
```

InfraLang statically checks what it knows. Provider schemas, provider attributes, many member/index expressions, module outputs, and Terraform function names remain dynamic. A successful `infralang check` does not replace `terraform validate`.

## Declaration Syntax

The following example shows the supported top-level declaration forms:

```infra
terraform {
  requiredVersion: ">= 1.5.0",
}

provider AWS from "hashicorp/aws" version "~> 6.0"

input region: string = "eu-central-1" with {
  description: "AWS region.",
}

let name = f"application-{region}"

configure aws = AWS({
  region: region,
})

configure awsEast = AWS("east", {
  region: "us-east-1",
})

data zones = aws.availabilityZones("available", {
  state: "available",
})

resource bucket = aws.s3Bucket("application", {
  bucket: name,
  tags: {
    "Name": name,
  },
}, {
  dependsOn: [],
  lifecycle: {
    preventDestroy: true,
  },
})

module child "child" from "./modules/child" {
  region: region,
} using {
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

- `terraform` supports `requiredVersion` or `required_version`; only one block is allowed.
- `provider` source and version are literal strings.
- `configure name = Provider({...})` creates the default provider configuration.
- `configure name = Provider("alias", {...})` creates an aliased configuration.
- `configure name = Provider` declares an inherited provider handle in a child module and emits no provider block.
- `resource` and `data` use the configured provider handle, not the provider declaration name.
- Resource and data Terraform labels are the first literal string in the constructor call. Keep them stable to preserve state addresses.
- A resource accepts an optional third object for Terraform meta-arguments. A data source does not.
- A module has both an InfraLang symbol and a separate quoted Terraform label: `module sourceName "terraform_label" ...`.
- Module `using` maps child provider configuration names to parent handles.
- Module `with` holds meta-arguments such as `forEach` and `dependsOn`.
- `moved` uses literal Terraform addresses. Its source commonly refers to infrastructure no longer declared in source.
- Declarations are order-independent and may be separated by whitespace or semicolons.

## Lexical Rules

- Source identifiers start with an ASCII letter or `_`, followed by letters, digits, or `_`.
- Static Terraform labels may additionally contain `-`, but cannot be computed.
- Strings use double quotes and Go-style escapes.
- Comments may use `#`, `//`, or `/* ... */`.
- Function arguments and array items require commas when multiple values are present.
- Object fields may be comma- or semicolon-separated; use trailing commas for readable multiline objects.
- Supported literals are strings, numbers, `true`, `false`, and `null`. Prefer documented `null` over the accepted legacy synonym `none`.

## Types And Metadata

Supported input types:

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
  required_field: string,
  optional_field?: number,
  defaulted_field?: number = 1024,
}
```

Use `dynamic` when no stronger type is available. Do not use the implementation alias `any` in new public examples.

Input defaults and object-field defaults must be compile-time constants. Object-field defaults are allowed only on fields marked `?`.

`optional<T>` without an explicit top-level input default lowers to a nullable Terraform variable with `default = null` and Terraform type `T`:

```infra
input cpu_mode: optional<string> with {
  description: "Optional CPU mode.",
  nullable: true,
  validations: [{
    condition: cpu_mode == null || contains(["host-model", "host-passthrough"], cpu_mode),
    errorMessage: "cpu_mode must be a supported mode.",
  }],
}
```

Input metadata supports:

- `description`
- `sensitive`
- `nullable`
- `validations`, an array of `{ condition, errorMessage }` objects

Output metadata supports `description` and `sensitive`.

## Expressions

InfraLang supports:

- Literal arrays and objects.
- Member access and indexing: `vm.id`, `vms[hostname]`.
- Direct Terraform function calls: `merge`, `concat`, `yamlencode`, `length`, and others.
- Unary `!` and `-`.
- Arithmetic, comparison, equality, and boolean operators.
- Null coalescing with `??`.
- Conditional expressions: `condition ? whenTrue : whenFalse`.
- List and object comprehensions.
- Formatted strings prefixed by `f`.

```infra
let enabledNames = [for name, machine in machines: name if machine.enabled]
let addresses = {for name, machine in machines: name => machine.ip_address}
let displayName = f"service-{environment}"
```

Regular strings are always literal. Use `f"...{expression}..."` for interpolation and `{{` or `}}` for literal braces inside formatted strings.

Operator precedence from lowest to highest is conditional, `??`, `||`, `&&`, equality, ordering, `+`/`-`, `*`/`/`/`%`, unary, then member/index/call postfix operations.

Use compiler-special `address()` for static Terraform traversals that must not become ordinary strings:

```infra
lifecycle: {
  ignoreChanges: [address("devices.consoles[0].source.pty.path")],
}
```

Pass exactly one literal string to `address()`.

## Terraform Lowering

InfraLang references lower as follows:

| InfraLang declaration | Terraform reference |
| --- | --- |
| `input name` | `var.name` |
| `let name` | `local.name` |
| `resource item = aws.s3Bucket("label", ...)` | `aws_s3_bucket.label` |
| `data item = aws.availabilityZones("label", ...)` | `data.aws_availability_zones.label` |
| `module child "stable" ...` | `module.stable` |

Provider method names become snake_case resource types. For example, `libvirt.domain` becomes `libvirt_domain` and `lvm.logicalVolume` becomes `lvm_logical_volume`.

Unquoted object keys passed to provider configurations, resources, and modules are recursively converted from camelCase to snake_case. Quoted keys are preserved:

```infra
{
  memoryUnit: "MiB",          # emits memory_unit
  tags: {
    "ApplicationName": name, # preserved exactly
  },
}
```

Quote arbitrary map keys whenever spelling or case must survive. Avoid pairs that collide after snake_case conversion.

Constant values become native JSON. Expressions become Terraform JSON interpolation expressions. Do not manually write Terraform interpolation syntax in ordinary InfraLang strings.

## Modules And Directories

All immediate `.infra` files in one directory form one InfraLang module. Directory commands recursively process local module sources beginning with `.` when the child directory itself contains `.infra` files.

```shell
bin/infralang check examples/staging
bin/infralang build examples/staging
```

Each compiled directory receives `main.tf.json`. A local or remote module containing only Terraform `.tf` files remains Terraform's responsibility.

Use `each.key` and `each.value` in module arguments only when that module declaration has `forEach`:

```infra
module vm "vm" from "../libvirt_vm" {
  hostname: each.key,
  config: each.value,
} using {
  libvirt: libvirt,
} with {
  forEach: machines,
  dependsOn: [network],
}
```

Current MVP limitation: the compiler installs `each` scope for module `forEach`, but not for resource `forEach`, and it does not install `count.index` scope.

## CLI Workflow

Build the current compiler before relying on its result:

```shell
mkdir -p bin
go build -o bin/infralang ./cmd/infralang
```

Use the CLI as follows:

```shell
bin/infralang check SOURCE.infra
bin/infralang check MODULE_DIR
bin/infralang build SOURCE.infra
bin/infralang build MODULE_DIR
bin/infralang build -stdout SOURCE.infra
bin/infralang build -o OUTPUT.tf.json SOURCE.infra
bin/infralang version
```

Place build flags before the source path. `-o` and `-stdout` are mutually exclusive and apply only to single-file builds.

For `.infra`-only edits:

1. Run `bin/infralang check PATH` first because it performs compilation without writing generated files.
2. Inspect output with `bin/infralang build -stdout FILE.infra` for a single source file.
3. Build a module directory only when generated artifacts are needed for Terraform/OpenTofu validation.
4. Run `terraform validate` or `tofu validate` when provider/module semantics matter and the required tooling is available.

For compiler, parser, lexer, or CLI changes:

```shell
go test ./...
go vet ./...
go build -o bin/infralang ./cmd/infralang
bin/infralang check RELEVANT_PATH
```

Add or update focused tests for new syntax, diagnostics, type rules, CLI behavior, and exact Terraform JSON lowering. Run Terraform validation integration tests where relevant.

## Safety And Pitfalls

- Never run `terraform apply` or `tofu apply` unless the user explicitly requests and approves real infrastructure changes.
- Treat plan operations, provider initialization, and storage examples as potentially environment-affecting. The LVM example can manage real logical volumes.
- Do not edit state files, `.terraform/`, lock files, or ignored generated JSON unless the task explicitly requires it.
- Preserve secrets: never expose provider credentials, variable values, state content, or authorization headers.
- Do not infer that `infralang check` validates provider field names or arbitrary Terraform function names.
- Do not put resource meta-arguments on `data` declarations.
- Do not use references or function calls as input or object-field defaults; defaults must be constant.
- Do not rename quoted resource, data, or module labels casually. Source symbol renames and Terraform address changes are separate concerns.
- Use `moved` blocks when an intentional Terraform address change must preserve existing state.
- Quote arbitrary nested map keys; unquoted keys are recursively snake-cased.
- Remember that a top-level `optional<T>` is nullable input behavior, not a general union type.
- Keep unsupported roadmap syntax out of `.infra` files: there are currently no imports, user-defined functions, components, formatter directives, native Terraform tests, or provider-schema-generated types.

## Canonical References

- Language guide: `docs/language.md`
- Project overview and CLI examples: `README.md`
- Lexer and parser: `internal/syntax/lexer.go`, `internal/syntax/parser.go`
- AST and tokens: `internal/syntax/ast.go`, `internal/syntax/token.go`
- Compiler and lowering: `internal/compiler/compiler.go`
- CLI and directory compilation: `cmd/infralang/main.go`
- End-to-end module example: `examples/staging/`
- Small language example: `examples/basic/main.infra`
- Provider alias example: `examples/provider-alias/`
