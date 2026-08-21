# InfraLang for Visual Studio Code

Desktop Visual Studio Code language support for `.infra` files.

## Features

- Registers `*.infra` files with the `infralang` language id.
- Highlights current InfraLang declarations, structural types, constants, static loops, components, comprehensions, contextual keywords, provider method calls, object keys, operators, raw moved addresses, and literals.
- Highlights `#`, `//`, and `/* ... */` comments.
- Highlights Go-escaped strings and `f"...{expression}..."` interpolation, including doubled literal braces.
- Starts an InfraLang language server over stdio and synchronizes open documents and all create, change, and delete events for `**/*.infra` files.
- Reports lexer, parser, name, type, component, and immediate-directory module diagnostics from unsaved editor contents.
- Completes InfraLang declarations, types, visible symbols, structural object members, components, component exports, local module outputs, and inputs of initialized Terraform modules.
- Completes installed provider configurations, resource types, data sources, arguments, nested block names, and result attributes from Terraform or OpenTofu schemas when enabled.
- Supports Go to Definition, hover, document symbols, and workspace symbols for InfraLang declarations and imported types.
- Formats valid InfraLang documents and enables InfraLang formatting on save by default.
- Writes client and server process logs to the **InfraLang Language Server** output channel.
- Provides **InfraLang: Restart Language Server** so path or argument changes can be applied without reloading the window.

Syntax highlighting and language configuration work even when the language server is disabled or unavailable. Diagnostics, navigation, completion, hover, and other protocol features depend on the capabilities implemented by the selected server.

Formatting on save uses this extension as the default formatter for `infralang` documents. A workspace or user setting can override `editor.formatOnSave` or `editor.defaultFormatter` when needed. Files with syntax errors are left unchanged.

## Installation

Install a packaged VSIX from the command line:

```shell
code --install-extension vscode-infralang-0.1.0-linux-x64.vsix
```

`npm run package` creates a target-specific VSIX for the current OS and architecture. Use `npm run package -- --target darwin-arm64` for one explicit target or `npm run package:all` for the supported release matrix. To use a separately built server, set `infralang.languageServer.path`.

## Development

Requirements are Node.js 20 or newer, npm, Go compatible with the parent InfraLang module, and Visual Studio Code 1.92 or newer.

From this directory:

```shell
npm install
npm run typecheck
npm run compile
npm test
npm run test-server
npm run build-server
npm run package
```

`npm run build-server` builds `./server` while Go discovers the parent repository's `go.mod`. `npm run test-server` tests the same server packages. Press `F5` in this directory to run the desktop extension host.

Release targets are `win32-x64`, `win32-arm64`, `linux-x64`, `linux-arm64`, `linux-armhf`, `alpine-x64`, `alpine-arm64`, `darwin-x64`, and `darwin-arm64`. Packaging sets matching `GOOS` and `GOARCH` values, disables CGO, and passes the same target to `vsce package`.

## Settings

| Setting | Default | Description |
| --- | --- | --- |
| `infralang.languageServer.enable` | `true` | Starts the language client when the extension activates. |
| `infralang.languageServer.path` | `""` | Executable path or command. Empty selects the bundled platform binary. `~/...` is expanded. |
| `infralang.languageServer.args` | `[]` | Arguments passed to the server process. |
| `infralang.languageServer.providerSchema.enable` | `true` | Loads locally installed provider schemas for completion in trusted workspaces. |

Run **InfraLang: Restart Language Server** after changing the server path or arguments. Disabling the server takes effect after a restart or window reload.

## Provider Schemas

InfraLang preserves Terraform and OpenTofu's provider ecosystem, but the InfraLang compiler itself does not load provider schemas. Provider method names are lowered from source-style names such as `aws.s3Bucket` to Terraform types such as `aws_s3_bucket`; unquoted argument keys are similarly converted to snake case.

In a trusted workspace, the language server runs `terraform providers schema -json`, falling back to `tofu providers schema -json`, in the current module directory. It does not initialize or download providers. A successful query supplies completion metadata for provider configuration, resource and data source names, writable arguments, nested block names, and result attributes. Failed queries are retried after a short cache interval. Disable this behavior with `infralang.languageServer.providerSchema.enable`.

Schema completion is not schema validation. Provider-specific value constraints, remote module interface validation, arbitrary Terraform function names, and Terraform graph semantics remain Terraform or OpenTofu's responsibility. Run `terraform validate` or `tofu validate`, and review a plan, after generating Terraform JSON.

## Terraform Module Inputs

After `terraform init` or `tofu init`, the language server reads the local `.terraform/modules/modules.json` manifest and the downloaded module configuration to complete module input keys. It does not initialize or download modules itself. Terraform snake-case input names are exposed in InfraLang source style, for example `bucket_prefix` as `bucketPrefix`.

## Limitations

- InfraLang and this extension are currently an MVP and should be evaluated before production use.
- Unsaved diagnostics combine all immediate `.infra` files in the edited directory. Full recursive local-module and type-import project validation still requires `infralang check`.
- Provider schema completion requires providers already installed for the module and may take a few seconds on first use.
- Terraform module input completion requires the module to be present in the local initialization manifest.
- The extension only invokes Terraform/OpenTofu for the read-only provider schema query described above. It never initializes providers, validates generated Terraform, plans, applies, or manages state.
- There is no browser/web extension, telemetry, automatic binary download, or remote service integration.
