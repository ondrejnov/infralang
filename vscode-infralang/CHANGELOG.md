# Changelog

All notable changes to the InfraLang Visual Studio Code extension are documented here.

## Unreleased

- Load provider schemas through an isolated cache before project initialization and refresh schemas automatically after Terraform artifacts change.
- Complete structural keys in typed input defaults, including imported type aliases.
- Complete the `if` keyword for conditional let-assignment blocks.
- Complete input keys and metadata for Terraform modules present in the local initialization manifest.
- Add document formatting and enable InfraLang formatting on save by default.

## 0.1.0

- Add InfraLang language registration and language configuration for `.infra` files.
- Add TextMate highlighting for the current InfraLang syntax.
- Add a desktop `vscode-languageclient` client with bundled or configurable server startup, full `.infra` file watching, output logging, and restart support.
- Add inline compiler diagnostics, completion for InfraLang symbols and installed provider schemas, Go to Definition, hover, document symbols, and workspace symbols.
- Add TypeScript, esbuild, server build/test scripts, client unit tests, and VSIX packaging configuration.
