# InfraLang Patterns

These examples are complete standalone patterns. Replace provider kinds and
attributes with those supported by the provider in the target Terraform
configuration. InfraLang statically checks language-level structure, while
Terraform validates provider-specific schemas.

## Basic Provider and Resource

```infra
terraform {
  requiredVersion: ">= 1.5.0",
}

provider AWS from "hashicorp/aws" version "~> 6.0"

input region: string = "eu-central-1"
input environment: string = "development"

configure aws = AWS({ region })

let name = f"application-{environment}"

resource bucket = aws.s3Bucket("application", {
  bucket: name,
  tags: {
    "Name": name,
    "Environment": environment,
  },
})

output bucketId = bucket.id
```

Use the source handle `bucket` in later expressions. The explicit label
`application` is the Terraform identity and should be changed only deliberately.

## Remote State Backend

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
```

Backend values must be compile-time constants (literals and `const`
references); Terraform itself forbids interpolations there, so runtime inputs
and locals are rejected with diagnostics. Exactly one backend clause is
allowed per module; a second one is diagnosed.

## Provider Alias and Child Mapping

```infra
terraform { requiredVersion: ">= 1.5.0" }

provider Null from "hashicorp/null" version "~> 3.0"
import module Child from "./child"

configure east = Null("east", {})

resource marker = east.resource("marker", {
  triggers: { "Region": "east" },
})

module child = Child("child", {
  marker: marker.id,
}) using {
  "null.east": east,
}
```

The child module must declare an inherited provider slot corresponding to the
mapped provider identity. A provider alias is part of provider configuration
identity and can affect state behavior.

## Checked Local Module

The parent module imports a child directory and passes typed inputs:

```infra
import module WebService from "./modules/web_service"

input hostname: string
input replicas: number = 2

module web = WebService("web", {
  hostname: hostname,
  replicas: replicas,
}) using { aws }

output endpoint = web.endpoint
```

Inside the child module, inherited provider configuration and inputs are
declared explicitly:

```infra
provider AWS from "hashicorp/aws" version "~> 6.0"
configure aws = AWS

input hostname: string
input replicas: number

resource service = aws.ecsService("web", {
  name: hostname,
  desiredCount: replicas,
})

output endpoint = service.endpoint
```

When parent and child use multiple `.infra` files in one directory, compile
the directory rather than assuming a single-file boundary. Local interfaces
check input names, types, outputs, provider slots, and cycles. A remote module
is intentionally not inspected by InfraLang.

## Checked Input Forwarding

Define a structural type once and forward it to a local module:

```infra
type ServiceConfig = object {
  hostname: string,
  replicas?: number = 2,
  tags?: map<string> = {},
}

input config: ServiceConfig
import module Service from "./service"

module service = Service("service", {
  ...inputs(config),
  replicas: 3,
})
```

`...inputs(config)` is checked against the child interface. Explicit fields
after the spread override forwarded fields. Use ordinary `...config` only for
runtime object composition, not interface forwarding.

## Runtime Iteration

Use runtime `forEach` when Terraform should determine instance cardinality:

```infra
type Worker = object {
  image: string,
  memory: number,
}

input workers: map<Worker>

resource vm = libvirt.domain("worker", {
  name: each.key,
  image: each.value.image,
  memory: each.value.memory,
}) with {
  forEach: workers,
}
```

The resource address is collection-shaped and Terraform manages instances by
runtime keys. Do not use `each` in the `forEach` expression or outside the
resource/module declaration.

## Compile-Time Constants and Static Loops

Use `const` and `static for` when declaration cardinality and labels should be
known before Terraform runs:

```infra
provider Null from "hashicorp/null" version "~> 3.0"

const regions = {
  east: { label: "east_marker" },
  west: { label: "west_marker" },
}

static for key, region in regions {
  configure providers[key] = Null({})
  resource marker = providers[key].resource(region.label, {
    triggers: { "Region": key },
  })
}
```

Object iteration is lexically ordered by wire key. Static loop expressions must
be compile-time evaluable: no inputs, provider values, Terraform functions,
file reads, or environment access.

## Component Without a State Boundary

```infra
provider Null from "hashicorp/null" version "~> 3.0"
configure null = Null({})

component Marker(label: string, value: string) using {
  null: Null,
} {
  resource item = null.resource(label, {
    triggers: { "Value": value },
  })

  export id = item.id
}

instantiate east = Marker(
  label: "east_marker",
  value: "east",
) using {
  null: null,
}

output eastId = east.id
```

The component expands into a direct `null_resource.east_marker`-style address
and does not create `module.*`. Component provider slots are checked against
provider source identities, not just local names.

## Type-Only Import and Component

```infra
import type { ServerConfig } from "./types.infra"

component Server(config: ServerConfig) using {
  null: Null,
} {
  resource item = null.resource(config.name, {
    triggers: { "Environment": config.environment },
  })

  export id = item.id
}
```

The imported file must export `ServerConfig` with `export type`. Type imports
add no runtime values and must use a relative `.infra` path inside the project.

## Intentional State Move

```infra
resource current = terraform.data("current", {
  input: "value",
})

moved from "terraform_data.previous" to "terraform_data.current"
```

Use a move only when the infrastructure identity changed intentionally and
existing state should follow. Review the generated address and plan; do not add
moves merely to silence an unrelated compiler error.
