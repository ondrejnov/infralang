# InfraLang Syntax Reference

This is a standalone reference for an AI authoring InfraLang. It describes
source syntax and lowering rules without relying on a repository checkout.

## Lexical Rules

- Identifiers start with an ASCII letter or `_` and continue with ASCII letters, digits, or `_`.
- Strings use double quotes and Go-style escapes.
- Backticks are reserved for raw Terraform addresses in grouped `moved` declarations.
- Comments are `#`, `//`, or `/* ... */`.
- Literals are strings, exact decimal numbers, `true`, `false`, and `null`.
- The legacy `none` spelling is accepted as null but should not be used in new source.
- Top-level declarations are separated by whitespace or semicolons.
- Function arguments and array items require commas between items.
- Object items may be comma- or semicolon-separated; use trailing commas in multiline objects.

## Declarations

```infra
terraform {
  requiredVersion: ">= 1.5.0",
  backend s3 = {
    bucket: "example-tfstate",
    key: "application/production/terraform.tfstate",
    dynamodbTable: "terraform-locks",
  },
  cloud = { organization: "acme" }
}

provider AWS from "hashicorp/aws" version "~> 6.0"

input region: string = "eu-central-1"
input imageId "image_id": string
input token: optional<string> with { sensitive: true }

let name = f"app-{region}"
const retryCount: number = 2 + 3

configure aws = AWS({ region })
configure awsEast = AWS("east", { region: "us-east-1" })
configure inherited = AWS

data zones = aws.availabilityZones("available", { state: "available" })
resource bucket = aws.s3Bucket("application", { bucket: name })
output bucketId = bucket.id
```

`terraform` accepts `requiredVersion` or `required_version`, but only one
Terraform settings block is allowed. It also accepts exactly one
`backend <type> = { ... }` clause and at most one `cloud = { ... }` clause.
Backend and cloud values must reduce to compile-time constants (literals and
`const` references; runtime values, function calls, and spreads are rejected).
Keys follow the normal object rule: unquoted camelCase converts to snake_case
on the wire, quoted keys keep their spelling. The clause emits ordinary
`"terraform": { "backend": { "s3": { ... } } }` / `"cloud": { ... }` JSON.
Provider source and version are literal strings. A provider configuration with
an alias uses the two-argument form. An inherited provider slot uses the
provider declaration without arguments and emits no child provider block.

## Types

```infra
string
number
bool
list<string>
set<number>
map<object { name: string }>
optional<string>
object {
  required: string,
  optional?: bool,
  defaulted?: number = 4,
}
```

Aliases are structural:

```infra
export type Server = object {
  ipAddress "ip_address": string,
  memory?: number = 1024,
}

type Servers = map<Server>
input servers: Servers
```

An optional top-level input without an explicit default emits an inner
Terraform type with `default = null`. Object-field defaults require `?` and
must be compile-time constants. Use `dynamic` when no stronger type is known.

## Source and Wire Keys

Unquoted names have a source spelling and a snake_case wire spelling in every
object context:

```infra
let config = {
  diskSizeGib: 20,
  sshAuthorizedKeys: [],
}

output disk = config.diskSizeGib
```

The wire keys are `disk_size_gib` and `ssh_authorized_keys`. Quoted keys have
no identifier-style source name and preserve their spelling:

```infra
let payload = { "X-Request-ID": requestId, "ApplicationName": "api" }
output request = payload["X-Request-ID"]
```

Use explicit aliases in structural types and inputs to preserve a specific
legacy wire name:

```infra
input imageId "imageId": string
type Image = object { imageId "imageId": string }
```

Provider method names use the same conversion. `aws.s3Bucket` emits resource
type `aws_s3_bucket`.

## Expressions

Runtime expressions support:

```infra
let enabled = flag && count > 0
let selected = value ?? fallback
let size = enabled ? 10 : 0
let values = [for name, item in items: item.value if item.enabled]
let index = {for name, item in items: name => item.id}
let merged = { ...base, region, retries: 3 }
let conditional = { debug: true when debugEnabled }
if (credentials != null) {
  merged = {
    ...merged,
    username: credentials.username,
    password: credentials.password,
  }
}
let rendered = yamlencode(merged)
```

Object punning (`{ region }`) is equivalent to `{ region: region }`. Spreads
and fields are applied left to right; later values win. `field: value when
condition` requires a boolean condition. A compile-time false condition removes
the field during preparation; a runtime condition lowers using Terraform merge
semantics. `if (condition) { name = value }` conditionally updates a previously
declared `let`; its body accepts assignments only. A target reference in the
right-hand side means the value before that assignment. Use resource `when`
rather than placing resource declarations inside `if`.

Operator precedence from lowest to highest is conditional, `??`, `||`, `&&`,
equality, ordering, `+`/`-`, `*`/`/`/`%`, unary, then member/index/call postfix
operations.

Formatted strings use `f` and interpolation:

```infra
let address = f"https://{host}:{port}/health"
let literal = f"{{not an interpolation}}"
```

`address("...")` is special only when it has exactly one literal string
argument. It marks a static Terraform traversal for metadata such as
`lifecycle.ignoreChanges`; other calls named `address` lower normally.

## Metadata

Inputs support `description`, `sensitive`, `nullable`, `validate`, and
`validations` metadata:

```infra
input hostname: string with {
  description: "VM hostname.",
  sensitive: false,
  validate length(trimspace(hostname)) > 0 else "hostname must not be empty.",
  validations: [{
    condition: length(hostname) <= 63,
    errorMessage: "hostname is too long.",
  }],
}
```

Validation messages must be static strings. Multiple validations retain source
order. Outputs support `description` and `sensitive` metadata.

Resource metadata is placed after the argument object in `with`:

```infra
resource vm = libvirt.domain("vm", {
  name: each.key,
}) with {
  forEach: machines,
  dependsOn: [network],
  lifecycle: { preventDestroy: true },
}
```

Modules use the same separation for Terraform module meta-arguments. Data
sources do not accept resource metadata.

## Resource Iteration

Runtime `forEach` gives typed `each` bindings:

```infra
resource vm = libvirt.domain("vm", {
  name: each.key,
  memory: each.value.memory,
}) with {
  forEach: machines,
}
```

For `map<T>`, `each.key` is `string` and `each.value` is `T`. Runtime
`forEach` also accepts objects and `set<string>` (where both fields are
`string`); other set element types are rejected. A runtime `forEach` cannot use
`each` in its own expression. `each` is unavailable outside the declaration.
`count.index` is not currently installed by InfraLang.

`when condition` is shorthand for a conditional resource collection using
`count`. It cannot be combined with explicit `count` or `forEach`.

## Moves

Both forms are valid:

```infra
moved from "module.old" to "module.current"

moved {
  `module.old["api"]` -> `module.current["api"]`,
  `null_resource.old` -> `terraform_data.current`,
}
```

Raw addresses are intentionally not resolved as current symbols because the old
address may no longer exist in source.
