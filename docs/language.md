# InfraLang language

InfraLang is a statically checked, expression-oriented frontend for Terraform.
It compiles `.infra` source files to Terraform JSON and leaves provider
execution, planning, state, and apply operations to Terraform or OpenTofu.

## Declarations

```infra
terraform {
  requiredVersion: ">= 1.5.0",
}

provider AWS from "hashicorp/aws" version "~> 6.0"

input region: string = "eu-central-1"
let name = f"application-{region}"

configure aws = AWS({
  region: region,
})

resource bucket = aws.s3Bucket("application", {
  bucket: name,
  tags: {
    "Name": name,
  },
})

output bucketId = bucket.id
```

Provider data sources use the same constructor convention:

```infra
data zones = aws.availabilityZones("available", {
  state: "available",
})

output zoneNames = zones.names
```

Top-level declarations can be separated by newlines or semicolons. Commas are
required between function arguments and recommended between object fields.

## Terraform modules

Existing Terraform modules are first-class InfraLang values:

```infra
module hostDb1 "host_db1" from "./modules/libvirt_host" {
  commonConfig: db1Common,
  networkName: "staging",
  vms: machines,
} using {
  libvirt: libvirtDb1,
  lvm: lvmDb1,
}

output machines = hostDb1.vms
```

The source identifier (`hostDb1`) is used by InfraLang expressions. The quoted
label (`host_db1`) is the Terraform module address, so existing state addresses
can be preserved.

## Types

Inputs support `string`, `number`, `bool`, `dynamic`, `list<T>`, `set<T>`,
`map<T>`, and `optional<T>`. InfraLang checks operations whose operand types are
known before generating Terraform configuration. Provider resource attributes
remain dynamic until provider-schema type generation is implemented.

An `optional<T>` input without an explicit default is lowered to a nullable
Terraform variable with `default = null` and the inner type constraint `T`.

## Expressions

InfraLang supports literals, arrays, objects, function calls, member access,
indexing, unary operators, arithmetic, comparisons, boolean operators, null
coalescing (`??`), and conditional expressions.

An interpolated string uses `f"...{expression}..."`. A regular string is always
literal. Terraform functions can be called directly, for example
`merge(base, overrides)`, `concat(first, second)`, or `yamlencode(value)`.

## Provider arguments

Unquoted object keys passed directly to provider configurations, resources, and
modules are converted from camelCase to snake_case. Quoted keys are preserved,
which is useful for arbitrary maps such as tags:

```infra
{
  memoryUnit: "MiB",
  tags: {
    "ApplicationName": "infralang",
  },
}
```

This compiles to `memory_unit` and preserves `ApplicationName`.

## Resource identity

The resource call consists of a provider configuration, a resource kind, a
static Terraform label, arguments, and optional Terraform meta-arguments:

```infra
resource vm = libvirt.domain("vm", {
  name: "worker-1",
}, {
  dependsOn: [rootImage],
  lifecycle: {
    preventDestroy: true,
  },
})
```

For provider `libvirt`, method `domain` becomes Terraform resource type
`libvirt_domain`. CamelCase method names are converted to snake_case, so
`lvm.logicalVolume` becomes `lvm_logical_volume`.

The optional third argument supports `count`, `forEach`, `dependsOn`,
`lifecycle`, and other Terraform resource meta-arguments.

## Current boundary

The initial implementation intentionally does not replace Terraform's graph or
state engine. User-defined functions, components, compile-time resource loops,
provider-schema generated types, imports, and state-move declarations are the
next language layers. Existing modules provide the composition boundary in the
MVP.
