# Checked input forwarding

Checked input forwarding is an InfraLang compile-time feature for passing a
typed object to a local child module:

```infra
import module Child from "./modules/child"

module child = Child("child", {
  ...inputs(config),
  hostname: overrideHostname,
})
```

`inputs` is not a Terraform function and is not emitted to Terraform JSON. The
InfraLang compiler expands the forwarding item into ordinary child-module
arguments after checking the object against the child's input interface.

## Where it can be used

The complete syntax is `...inputs(value)`. It is valid only as an item in a
module argument object:

```infra
module child = Child("child", {
  ...inputs(config),
})
```

It cannot be called as an ordinary expression or used in a general object:

```infra
# Invalid: inputs forwarding is only valid in module arguments.
let copied = {
  ...inputs(config),
}
```

The child must be a local InfraLang module whose canonical directory contains
at least one `.infra` file. Registry modules, remote modules, and local
Terraform-only modules do not expose an interface that InfraLang can use for
checked forwarding.

## Structural requirements

The forwarded value must have a statically known structural object type. A
named object type, an object literal, or a typed `each.value` can satisfy this
requirement:

```infra
type ServiceConfig = object {
  imageId: string,
  memoryMb?: number = 1024,
}

input services: map<ServiceConfig>

module services = Service("service", {
  ...inputs(each.value),
  name: each.key,
}) with {
  forEach: services,
}
```

An open-ended value such as `map<dynamic>` is not sufficient because its keys
are not known statically:

```infra
input config: map<dynamic>

# Invalid: config is a map, not a structural object.
module child = Child("child", {
  ...inputs(config),
})
```

## Matching rules

InfraLang compares each forwarded object's wire key with the child input's wire
name. Unquoted source names are converted to snake_case, so these declarations
match without an explicit alias:

```infra
# Parent object type
type Config = object {
  imageId: string,
}

# Child module input
input imageId: string
```

Both use `image_id` on the Terraform wire. Explicit aliases can preserve a
legacy or otherwise deliberate wire name:

```infra
# Parent object type
type Config = object {
  imageId "legacyImage": string,
}

# Child module input
input imageId "legacyImage": string
```

The compiler checks:

- Every forwarded field is a known child input.
- Every required child input is supplied by forwarding or explicitly.
- Forwarded and explicit values are assignable to the child input types.
- A child input is not contributed by multiple forwarding items.
- Child input wire names are used in generated Terraform JSON.

Place forwarding items before explicit arguments when an explicit value should
override a forwarded field:

```infra
module child = Child("child", {
  ...inputs(config),
  retries: 5,
})
```

This pattern is useful when most fields come from a reusable configuration but
the caller owns one or two contextual values such as a hostname, label, or
iteration key.

## Generated Terraform

Given this parent configuration:

```infra
type Config = object {
  imageId: string,
  retries: number,
}

input config: Config
import module Child from "./child"

module child = Child("child", {
  ...inputs(config),
})
```

and these child inputs:

```infra
input imageId: string
input retries: number = 1
```

the generated module block contains ordinary Terraform arguments equivalent to:

```json
{
  "module": {
    "child": {
      "source": "./child",
      "image_id": "${var.config.image_id}",
      "retries": "${var.config.retries}"
    }
  }
}
```

No `inputs` function or forwarding metadata remains in the generated output.

## Forwarding vs. object spread

Use ordinary object spread to compose InfraLang objects:

```infra
let merged = {
  ...defaults,
  enabled: true,
}
```

Use checked input forwarding only at a local module boundary:

```infra
module child = Child("child", {
  ...inputs(merged),
})
```

Ordinary spread combines object values. Checked input forwarding additionally
uses the child interface to reject unknown, missing, duplicate, and
type-incompatible arguments before Terraform runs.

## Validation workflow

Check the project directory that contains both the caller and every referenced
local child module:

```shell
infralang check path/to/project
```

Checking only a nested module directory may reject sibling imports that resolve
outside that directory. A project-wide check discovers the complete local
module graph and all child interfaces.

For the shorter language-reference summary, see
[Local module interfaces](language.md#local-module-interfaces).
