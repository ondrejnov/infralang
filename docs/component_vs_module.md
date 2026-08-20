# Components vs. modules

In InfraLang, the main difference between a component and a module is when the
construct is expanded and whether it remains a boundary in generated Terraform.

| | Module | Component |
| --- | --- | --- |
| Purpose | A separate infrastructure unit | A reusable declaration template |
| Scope | Has its own directory and interface | Is scoped to the current directory module |
| Parameters | Child-module `input` and `output` declarations | Typed parameters and `export` declarations |
| Terraform output | Remains a Terraform `module` | Is expanded during compilation and disappears |
| Terraform state | Creates a namespace such as `module.vm` | Does not create a namespace or state boundary |
| Source | A local directory, registry module, or URL | A local `component` declaration |
| Checking | Local InfraLang modules have checked interfaces; remote modules are unchecked boundaries | Arguments, exports, and provider slots are statically checked |

## Modules

All immediate `.infra` files in one directory form one InfraLang module. A
`module` declaration instantiates another module, for example:

```infra
module vm "production_vm" from "./modules/vm" {
  hostname: "server-01",
} using [libvirt]
```

The child module has its own interface, primarily defined by `input`, `output`,
and inherited provider-slot declarations. A local child directory containing
InfraLang files is recursively compiled and its interface is checked. Registry
modules, remote URLs, and local Terraform-only directories are explicit
unchecked interface boundaries.

The declaration remains a module in generated Terraform and contributes a
module namespace to resource addresses, for example:

```text
module.production_vm.libvirt_domain.server
```

## Components

A component is a typed declaration template within the current directory
module:

```infra
component Server(label: string, hostname: string) using {
  libvirt: Libvirt,
} {
  resource vm = libvirt.domain(label, {
    name: hostname,
  })

  export id = vm.id
}

instantiate server = Server(
  label: "production_vm",
  hostname: "server-01",
) using {
  libvirt: libvirt,
}
```

InfraLang checks component arguments, structural types, exports, and provider
slots. During compilation, the component is expanded into ordinary resources,
modules, and provider mappings. The component declaration and instance then
disappear before Terraform JSON is emitted.

Component expansion does not introduce a Terraform module or a state
namespace. The resource from the example therefore has an address such as:

```text
libvirt_domain.production_vm
```

`server.id` is a typed InfraLang handle, not a Terraform output. To expose it
from the current module, declare an output explicitly:

```infra
output serverId = server.id
```

## Which one should I use?

Use a **component** to remove repetition while keeping the expanded resources
in the current Terraform module and preserving their direct resource addresses.

Use a **module** when you need a separate infrastructure unit with its own
directory, input/output interface, and Terraform state namespace.
