# InfraLang Troubleshooting

## Verification Sequence

1. Confirm the source has the `.infra` extension and the executable is available with `infralang version`.
2. Run `infralang check PATH` on the file or, for a directory module, on the directory.
3. If the directory has multiple `.infra` files, always validate the directory so imports and local interfaces are included.
4. Run `infralang build -stdout FILE.infra` when reviewing one file without writing an artifact.
5. Build the module directory when Terraform JSON artifacts are required.
6. Run `terraform validate` or `tofu validate` for provider schemas, remote modules, and Terraform-specific behavior.
7. Review `terraform plan` or `tofu plan` before any approved apply.

`check` does not write Terraform JSON. A diagnostic prevents artifact emission.
Directory builds produce `main.tf.json` per compiled module; single-file builds
produce a sibling `.tf.json` unless `-o` is supplied.

## Command Failures

### `infralang: missing command` or unknown command

Use one of `build`, `check`, `fmt`, `init`, `validate`, `plan`, `output`,
`apply`, `destroy`, `version`, or `help`. Put build flags before the source path:

```shell
infralang build -stdout main.infra
```

`-o` and `-stdout` cannot be combined and are supported only for a single file.

### Source must use `.infra`

InfraLang compiles `.infra` source. Terraform `.tf` files can coexist as an
explicit boundary, but they are not parsed as InfraLang.

### A file build ignores a sibling file

Immediate `.infra` files in one directory form a single module. A file next to
another `.infra` file is not an isolated compilation unit; compile the directory
or let the CLI detect the module directory.

### `terraform validate` fails after `infralang check` succeeds

This is expected when the issue is outside InfraLang's static boundary. Check:

- provider resource/data attribute names and types;
- nested provider blocks and required fields;
- provider installation and version constraints;
- remote module source and its inputs/outputs;
- Terraform-only child directories;
- Terraform function availability and runtime expression semantics.

Run Terraform/OpenTofu validation in the generated configuration directory.

## Syntax and Type Errors

### Unknown name or wrong handle

Resource and data declarations reference configured provider handles:

```infra
provider AWS from "hashicorp/aws" version "~> 6.0"
configure aws = AWS({ region })
resource bucket = aws.s3Bucket("bucket", {})
```

The provider declaration name `AWS` is not the runtime handle `aws`.

### Provider mapping error

Check that the child provider slot is mapped to a parent configuration with the
same provider source identity. Local name equality alone is not sufficient when
aliases or different provider declarations are involved.

### Missing, unknown, or incompatible module input

For a local module, compare the parent's argument object with the child's input
interface. Use explicit fields or checked forwarding:

```infra
module child = Child("child", {
  ...inputs(config),
  hostname: overrideName,
})
```

`...inputs` works only inside a module argument object and requires a statically
known structural object. Later explicit fields override forwarded ones.

### Type import cannot be resolved

Type imports must:

- use a relative path;
- name a real `.infra` file, including the extension;
- import an alias declared with `export type`;
- remain inside the project root after canonicalization and symlink resolution;
- avoid duplicate imports, name collisions, and cycles.

Type imports add no runtime declarations.

### Module import version rejected

A `version` clause on `import module` must:

- be a non-empty string literal (`version ""` is a parse error);
- not appear on a local source beginning with `./`, `../`, or `/`;
- agree across all imports of the same remote source — pinning one import and
  leaving another unversioned is also a conflict, because Terraform resolves
  one version per source.

### `each` is unavailable

`each` exists only inside a resource or module declaration with runtime
`forEach`. It is not available in a `forEach` expression itself, in locals, in
constants, or in a declaration without `forEach`.

### Constant or default rejected

Constants and defaults may use only compile-time values. Remove references to
inputs, locals, resources, modules, component instances, runtime iterators,
Terraform functions, file contents, and environment variables. Use a runtime
`let` or declaration expression when the value is intentionally evaluated by
Terraform.

### Conditional resource access fails

`when condition` creates a `count`-based collection. Index it before direct
attribute access or iterate over it. Do not combine `when` with `count` or
`forEach`.

### Wire key or input name is unexpected

Unquoted source names lower to snake_case everywhere. Quote an object key to
preserve exact spelling, or use an explicit alias in an input/type field:

```infra
input imageId "imageId": string
let value = { "imageId": imageId }
```

Access quoted keys with string indexing, for example `value["imageId"]`.

## State and Address Problems

Before changing a resource, data source, module label, provider alias, static
label key, or runtime cardinality, inspect the current Terraform address. Source
handle renames alone do not change Terraform identity; explicit labels and
provider aliases do.

If state must follow an intentional address change, add a raw `moved` declaration
and verify it in the plan. InfraLang does not infer state migrations.

Components do not add a module namespace. Refactoring direct declarations into a
component can preserve state only if the expanded labels and provider aliases
remain unchanged.

## Safety Checks

- Never expose or commit provider credentials, state files, private keys, or sensitive defaults.
- Do not edit `.tf.json` to repair a source error; fix `.infra` and rebuild.
- Do not use `apply` or `destroy` as a validation command.
- Do not assume a successful compiler check means a safe plan.
- Inspect generated addresses and the Terraform plan when labels, aliases, module sources, or iteration change.
