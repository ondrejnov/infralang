package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFramingRoundTrip(t *testing.T) {
	var output bytes.Buffer
	writer := newFramedWriter(&output)
	want := map[string]any{"jsonrpc": "2.0", "id": 7, "result": "ok"}
	if err := writer.Write(want); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	payload, err := newFramedReader(&output).Read()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got["result"] != "ok" || got["id"] != float64(7) {
		t.Fatalf("unexpected payload: %#v", got)
	}
}

func TestFramingRejectsMissingContentLength(t *testing.T) {
	_, err := newFramedReader(strings.NewReader("X-Test: true\r\n\r\n{}")).Read()
	if err == nil || !strings.Contains(err.Error(), "Content-Length") {
		t.Fatalf("expected Content-Length error, got %v", err)
	}
}

func TestInitializeShutdownLifecycle(t *testing.T) {
	root := t.TempDir()
	input := bytes.NewBuffer(nil)
	for _, message := range []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
			"rootUri": pathToURI(root), "initializationOptions": map[string]any{"providerSchemas": false},
		}},
		{"jsonrpc": "2.0", "id": 2, "method": "shutdown"},
		{"jsonrpc": "2.0", "method": "exit"},
	} {
		if err := newFramedWriter(input).Write(message); err != nil {
			t.Fatalf("write request: %v", err)
		}
	}
	var output bytes.Buffer
	if code := newServer(input, &output).run(); code != 0 {
		t.Fatalf("server exit code = %d", code)
	}
	reader := newFramedReader(&output)
	for _, id := range []float64{1, 2} {
		payload, err := reader.Read()
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var response map[string]any
		if err := json.Unmarshal(payload, &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if response["id"] != id || response["error"] != nil {
			t.Fatalf("unexpected response: %#v", response)
		}
	}
}

func TestUTF16Positions(t *testing.T) {
	source := "a😀b\nç"
	position := positionAt(source, len("a😀"))
	if position.Line != 0 || position.Character != 3 {
		t.Fatalf("position after astral rune = %#v, want line 0 character 3", position)
	}
	if offset := offsetAt(source, position); offset != len("a😀") {
		t.Fatalf("round-trip offset = %d, want %d", offset, len("a😀"))
	}
	if offset := offsetAt(source, Position{Line: 1, Character: 1}); offset != len(source) {
		t.Fatalf("second-line offset = %d, want %d", offset, len(source))
	}
}

func TestDocumentFormattingUsesOpenDocumentAndReturnsFullEdit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	writeTestFile(t, path, "input oldValue: string\n")
	workspace := newWorkspace()
	uri := pathToURI(path)
	source := "input   region:string=\"eu-central-1\""
	if _, err := workspace.open(uri, source, 2); err != nil {
		t.Fatalf("open document: %v", err)
	}

	edits := (&server{workspace: workspace}).formatDocument(uri)
	if len(edits) != 1 {
		t.Fatalf("formatDocument() edits = %#v, want one edit", edits)
	}
	if got, want := edits[0].NewText, "input region: string = \"eu-central-1\"\n"; got != want {
		t.Fatalf("formatted text = %q, want %q", got, want)
	}
	if got, want := edits[0].Range.End, positionAt(source, len(source)); got != want {
		t.Fatalf("edit end = %#v, want %#v", got, want)
	}
}

func TestDocumentFormattingReturnsNoEditForInvalidSource(t *testing.T) {
	workspace := newWorkspace()
	uri := pathToURI(filepath.Join(t.TempDir(), "main.infra"))
	if _, err := workspace.open(uri, "input =", 1); err != nil {
		t.Fatalf("open document: %v", err)
	}
	if edits := (&server{workspace: workspace}).formatDocument(uri); len(edits) != 0 {
		t.Fatalf("formatDocument() edits = %#v, want none", edits)
	}
}

func TestWorkspaceIndexIncludesModuleAndNestedDeclarations(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "defs.infra"), `
provider AWS from "hashicorp/aws"
component Deployment(config: object { endpoint: string }) {
  let internal = config.endpoint
  export endpoint = internal
}
static for name in ["one"] {
  let nested = name
}
`)
	writeTestFile(t, filepath.Join(root, "main.infra"), `
configure aws = AWS({})
let settings = { endpoint: "example.test" }
instantiate deployment = Deployment(config: settings)
`)
	writeTestFile(t, filepath.Join(root, ".hidden", "ignored.infra"), `let ignored = true`)
	writeTestFile(t, filepath.Join(root, "node_modules", "ignored.infra"), `let ignoredNode = true`)

	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	if err := workspace.scan(); err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	visible := (&server{workspace: workspace}).visibleSymbols(root)
	for _, name := range []string{"AWS", "Deployment", "nested", "aws", "settings", "deployment"} {
		if visible[name] == nil {
			t.Errorf("missing indexed symbol %q", name)
		}
	}
	if visible["ignored"] != nil || visible["ignoredNode"] != nil {
		t.Fatalf("index included symbols from skipped directories")
	}
	component := workspace.file(filepath.Join(root, "defs.infra")).Components["Deployment"]
	if component == nil || component.Fields["endpoint"] == nil {
		t.Fatalf("component export was not indexed: %#v", component)
	}
}

func TestDefinitionResolvesLocalModuleImportPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "modules", "host", "main.infra")
	writeTestFile(t, path, `import module LibvirtVm from "../libvirt_vm"
`)
	target := filepath.Join(root, "modules", "libvirt_vm", "main.infra")
	writeTestFile(t, target, "output id = \"value\"\n")

	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	if err := workspace.scan(); err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	server := &server{workspace: workspace}
	source := workspace.file(path).Source
	offset := strings.Index(source, "libvirt_vm") + 1
	locations := server.definition(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Position:     positionAt(source, offset),
	})
	if len(locations) != 1 || locations[0].URI != pathToURI(target) {
		t.Fatalf("local module import definition = %#v, want %q", locations, pathToURI(target))
	}
}

func TestDefinitionDoesNotResolveRemoteModuleImportPathAsFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	writeTestFile(t, path, `import module LibvirtVm from "registry.example/libvirt_vm"
`)

	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	if err := workspace.scan(); err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	server := &server{workspace: workspace}
	source := workspace.file(path).Source
	offset := strings.Index(source, "libvirt_vm") + 1
	locations := server.definition(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(path)},
		Position:     positionAt(source, offset),
	})
	if len(locations) != 0 {
		t.Fatalf("remote module import path definition = %#v, want no locations", locations)
	}
}

func TestCompletionUsesOverlayAndStructuralMembers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	writeTestFile(t, path, "let oldValue = true\n")
	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	if err := workspace.scan(); err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	source := "let settings = { displayName: \"api\", port: 80 }\noutput selected = settings."
	uri := pathToURI(path)
	if _, err := workspace.open(uri, source, 2); err != nil {
		t.Fatalf("open overlay: %v", err)
	}
	server := &server{workspace: workspace}
	items := server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(source, len(source)),
	})
	labels := completionLabels(items)
	if !labels["displayName"] || !labels["port"] {
		t.Fatalf("member completions = %v", labels)
	}
	if labels["oldValue"] {
		t.Fatalf("completion used disk content instead of open overlay")
	}
}

func TestCompletionUsesComposedTypeMembers(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	writeTestFile(t, path, "input config: Config\n")
	writeTestFile(t, filepath.Join(root, "base.infra"), `type Base = object { host: string }`)
	writeTestFile(t, filepath.Join(root, "types.infra"), `
type Config = Base & object { port: number }
`)
	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	if err := workspace.scan(); err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	source := `
input config: Config
output selected = config.
`
	uri := pathToURI(path)
	if _, err := workspace.open(uri, source, 1); err != nil {
		t.Fatalf("open overlay: %v", err)
	}
	labels := completionLabels((&server{workspace: workspace}).completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(source, strings.Index(source, "config.")+len("config.")),
	}))
	if !labels["host"] || !labels["port"] {
		t.Fatalf("composed type member completions = %v", labels)
	}
}

func TestCompletionIncludesConditionalAssignmentKeyword(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	workspace := newWorkspace()
	uri := pathToURI(path)
	if _, err := workspace.open(uri, "", 1); err != nil {
		t.Fatalf("open document: %v", err)
	}
	labels := completionLabels((&server{workspace: workspace}).completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: Position{},
	}))
	if !labels["if"] {
		t.Fatalf("top-level completions omit conditional assignment keyword: %v", labels)
	}
}

func TestCompletionUsesComponentScope(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "component.infra")
	source := `component Deployment(config: object { endpoint: string }) {
  let selected = config.
}`
	workspace := newWorkspace()
	uri := pathToURI(path)
	if _, err := workspace.open(uri, source, 1); err != nil {
		t.Fatalf("open document: %v", err)
	}
	server := &server{workspace: workspace}
	offset := strings.Index(source, "config.") + len("config.")
	labels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(source, offset),
	}))
	if !labels["endpoint"] {
		t.Fatalf("component parameter member missing from completion: %v", labels)
	}
}

func TestCompletionUsesComponentParametersInInstantiateArguments(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	writeTestFile(t, filepath.Join(root, "component.infra"), `
type DeploymentConfig = object {
  endpoint: string,
  port?: number = 443,
}
component Deployment(hostname: string, config: DeploymentConfig) {
  export selected = hostname
}
`)
	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	if err := workspace.scan(); err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	server := &server{workspace: workspace}
	uri := pathToURI(path)

	partialSource := `instantiate deployment = Deployment(
  host
)`
	if _, err := workspace.open(uri, partialSource, 1); err != nil {
		t.Fatalf("open partial component instance: %v", err)
	}
	partialOffset := strings.Index(partialSource, "host\n") + len("host")
	partialLabels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(partialSource, partialOffset),
	}))
	if !partialLabels["hostname"] || !partialLabels["config"] {
		t.Fatalf("partial instantiate argument completions = %v", partialLabels)
	}
	if partialLabels["instantiate"] || partialLabels["output"] {
		t.Fatalf("non-parameter completions leaked into instantiate arguments: %v", partialLabels)
	}

	nestedSource := `instantiate deployment = Deployment(
  hostname: "api",
  config: {

  },
)`
	if _, err := workspace.change(uri, nestedSource, 2); err != nil {
		t.Fatalf("change component instance: %v", err)
	}
	nestedOffset := strings.Index(nestedSource, "{\n\n") + len("{\n")
	nestedLabels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(nestedSource, nestedOffset),
	}))
	if !nestedLabels["endpoint"] || !nestedLabels["port"] || nestedLabels["hostname"] || nestedLabels["config"] {
		t.Fatalf("nested instantiate argument completions = %v", nestedLabels)
	}
}

func TestCompletionFollowsIndexedComponentsAndChainedFields(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	source := `
component Deployment() {
  export details = { endpoint: { host: "example.test" } }
}
instantiate deployments["api"] = Deployment()
output host = deployments["api"].details.endpoint.
`
	workspace := newWorkspace()
	uri := pathToURI(path)
	if _, err := workspace.open(uri, source, 1); err != nil {
		t.Fatalf("open document: %v", err)
	}
	server := &server{workspace: workspace}
	labels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(source, len(source)-1),
	}))
	if !labels["host"] {
		t.Fatalf("indexed chained completion = %v", labels)
	}
}

func TestCompletionUsesNestedImportedStructuralTypes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	writeTestFile(t, filepath.Join(root, "types.infra"), `
import type { VmConfig } from "./child/types.infra"
type HostBase = object {
  gateway: string,
}
type HostDefinition = HostBase & object {
  vms: map<VmConfig>,
}
`)
	writeTestFile(t, filepath.Join(root, "child", "types.infra"), `
export type VmConfig = object {
  ipAddress: string,
  ramMb?: number = 1024,
}
`)
	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	if err := workspace.scan(); err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	server := &server{workspace: workspace}
	uri := pathToURI(path)

	hostSource := `input remoteHost: string
output visibleOutput = "value"
const hosts: map<HostDefinition> = {
  db1: {
    
    vms: { app: { ipAddress: "10.0.0.1" } },
  },
}`
	if _, err := workspace.open(uri, hostSource, 1); err != nil {
		t.Fatalf("open host source: %v", err)
	}
	hostOffset := strings.Index(hostSource, "    \n") + 4
	hostLabels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(hostSource, hostOffset),
	}))
	if !hostLabels["gateway"] || hostLabels["ramMb"] {
		t.Fatalf("HostDefinition completions = %v", hostLabels)
	}
	if hostLabels["remoteHost"] || hostLabels["visibleOutput"] || hostLabels["output"] {
		t.Fatalf("non-field completions leaked into HostDefinition keys: %v", hostLabels)
	}

	vmSource := `input remoteHost: string
output visibleOutput = "value"
const hosts: map<HostDefinition> = {
  db1: {
    gateway: "10.0.0.254",
    vms: {
      app: {
        
        ipAddress: "10.0.0.1",
      },
    },
  },
}`
	if _, err := workspace.change(uri, vmSource, 2); err != nil {
		t.Fatalf("change VM source: %v", err)
	}
	vmOffset := strings.Index(vmSource, "        \n") + 8
	vmLabels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(vmSource, vmOffset),
	}))
	if !vmLabels["ramMb"] || vmLabels["gateway"] || vmLabels["ipAddress"] {
		t.Fatalf("VmConfig completions = %v", vmLabels)
	}
	if vmLabels["remoteHost"] || vmLabels["visibleOutput"] || vmLabels["output"] {
		t.Fatalf("non-field completions leaked into VmConfig keys: %v", vmLabels)
	}
}

func TestCompletionUsesImportedStructuralTypeInInputDefault(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	writeTestFile(t, filepath.Join(root, "types.infra"), `
export type VmDefaults = object {
  baseImagePath: string,
  remoteUser?: string = "root",
}
`)
	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	if err := workspace.scan(); err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	source := `import type { VmDefaults as PokusVmDefaults } from "./types.infra"
input vmDefaults: PokusVmDefaults = {
  bas
}`
	uri := pathToURI(path)
	if _, err := workspace.open(uri, source, 1); err != nil {
		t.Fatalf("open input default: %v", err)
	}
	offset := strings.Index(source, "bas\n") + len("bas")
	labels := completionLabels((&server{workspace: workspace}).completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(source, offset),
	}))
	if !labels["baseImagePath"] || !labels["remoteUser"] {
		t.Fatalf("input default completions = %v", labels)
	}
	if labels["input"] || labels["vmDefaults"] {
		t.Fatalf("non-field completions leaked into input default keys: %v", labels)
	}
}

func TestCompletionUsesProviderSchemaCache(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	source := `
provider AWS from "hashicorp/aws"
configure aws = AWS({})
resource bucket = aws.s3Bucket("bucket", {
  
})
`
	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	uri := pathToURI(path)
	if _, err := workspace.open(uri, source, 1); err != nil {
		t.Fatalf("open document: %v", err)
	}
	workspace.schemas.entries[root] = &schemaCacheEntry{
		requirements: providerRequirementKey([]providerRequirement{{LocalName: "aws", Source: "hashicorp/aws"}}),
		providers: parseTerraformSchemas([]byte(`{
  "provider_schemas": {
    "registry.terraform.io/hashicorp/aws": {
      "provider": {"block": {
        "attributes": {
          "region": {"optional": true},
          "account_id": {"computed": true}
        },
        "block_types": {
          "ssh": {"nesting_mode": "single", "block": {"attributes": {
            "host": {"optional": true},
            "user": {"optional": true}
          }}}
        }
      }},
      "resource_schemas": {
        "aws_s3_bucket": {"block": {"attributes": {
          "bucket_prefix": {"optional": true, "description": "Prefix"},
          "id": {"computed": true}
        }}}
      },
      "data_source_schemas": {
        "aws_caller_identity": {"block": {"attributes": {"account_id": {"computed": true}}}}
      }
    }
  }
}`)),
	}
	server := &server{workspace: workspace}
	position := positionAt(source, strings.Index(source, "  \n}")+2)
	labels := completionLabels(server.completions(TextDocumentPositionParams{TextDocument: TextDocumentIdentifier{URI: uri}, Position: position}))
	if !labels["bucketPrefix"] {
		t.Fatalf("schema completion missing camelCase attribute: %v", labels)
	}
	if labels["id"] || labels["if"] {
		t.Fatalf("non-resource-key completion was offered as a resource argument: %v", labels)
	}

	memberSource := source + "\noutput bucketId = bucket."
	if _, err := workspace.change(uri, memberSource, 2); err != nil {
		t.Fatalf("change document: %v", err)
	}
	memberLabels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(memberSource, len(memberSource)),
	}))
	if !memberLabels["id"] || !memberLabels["bucketPrefix"] {
		t.Fatalf("resource result attributes missing from completion: %v", memberLabels)
	}

	aliasSource := source + "\nlet volume = bucket[0]\noutput volumeId = volume."
	if _, err := workspace.change(uri, aliasSource, 2); err != nil {
		t.Fatalf("change aliased resource source: %v", err)
	}
	aliasLabels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(aliasSource, len(aliasSource)),
	}))
	if !aliasLabels["id"] || !aliasLabels["bucketPrefix"] {
		t.Fatalf("indexed resource alias completion = %v", aliasLabels)
	}

	configSource := `
provider AWS from "hashicorp/aws"
configure aws = AWS({
  
})
`
	if _, err := workspace.change(uri, configSource, 3); err != nil {
		t.Fatalf("change provider configuration: %v", err)
	}
	configPosition := positionAt(configSource, strings.Index(configSource, "  \n}")+2)
	configLabels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: configPosition,
	}))
	if !configLabels["region"] || configLabels["accountId"] {
		t.Fatalf("provider argument completions = %v", configLabels)
	}

	nestedConfigSource := `
provider AWS from "hashicorp/aws"
input remoteHost: string
output visibleOutput = "value"
configure aws = AWS({
  ssh: {
    
  },
})
`
	if _, err := workspace.change(uri, nestedConfigSource, 4); err != nil {
		t.Fatalf("change nested provider configuration: %v", err)
	}
	nestedPosition := positionAt(nestedConfigSource, strings.Index(nestedConfigSource, "    \n")+4)
	nestedLabels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: nestedPosition,
	}))
	if !nestedLabels["host"] || !nestedLabels["user"] || nestedLabels["region"] || nestedLabels["ssh"] {
		t.Fatalf("nested provider argument completions = %v", nestedLabels)
	}
	if nestedLabels["remoteHost"] || nestedLabels["visibleOutput"] || nestedLabels["output"] {
		t.Fatalf("non-schema completions leaked into provider keys: %v", nestedLabels)
	}

	valueSource := `
provider AWS from "hashicorp/aws"
input remoteHost: string
configure aws = AWS({
  ssh: {
    host: remoteHost,
  },
})
`
	if _, err := workspace.change(uri, valueSource, 5); err != nil {
		t.Fatalf("change provider value: %v", err)
	}
	valueOffset := strings.Index(valueSource, "remoteHost,") + len("remoteHost")
	valueLabels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(valueSource, valueOffset),
	}))
	if !valueLabels["remoteHost"] || valueLabels["host"] || valueLabels["user"] {
		t.Fatalf("provider value completions = %v", valueLabels)
	}
	colonOffset := strings.Index(valueSource, "host: ") + len("host: ")
	colonLabels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(valueSource, colonOffset),
	}))
	if !colonLabels["remoteHost"] || colonLabels["host"] || colonLabels["user"] {
		t.Fatalf("provider completions immediately after colon = %v", colonLabels)
	}
}

func TestResourceMetaArgumentCompletions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	source := `provider AWS from "hashicorp/aws"
configure aws = AWS({})
resource bucket = aws.s3Bucket("bucket", {})
resource volume = aws.ebsVolume("volume", {
}) with {
  
}
`
	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	uri := pathToURI(path)
	if _, err := workspace.open(uri, source, 1); err != nil {
		t.Fatalf("open document: %v", err)
	}
	server := &server{workspace: workspace}
	metaOffset := strings.Index(source, "  \n}") + 2
	labels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(source, metaOffset),
	}))
	for _, label := range []string{"count", "dependsOn", "forEach", "lifecycle"} {
		if !labels[label] {
			t.Fatalf("resource meta-argument %q missing: %v", label, labels)
		}
	}
	if labels["bucket"] {
		t.Fatalf("resource argument leaked into meta-arguments: %v", labels)
	}

	lifecycleSource := source[:strings.Index(source, "  \n}")-1] + "  lifecycle: {\n    \n  },\n}\n"
	if _, err := workspace.change(uri, lifecycleSource, 2); err != nil {
		t.Fatalf("change document: %v", err)
	}
	lifecycleOffset := strings.Index(lifecycleSource, "    \n") + 4
	lifecycleLabels := completionLabels(server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(lifecycleSource, lifecycleOffset),
	}))
	for _, label := range []string{"createBeforeDestroy", "ignoreChanges", "preventDestroy", "replaceTriggeredBy"} {
		if !lifecycleLabels[label] {
			t.Fatalf("lifecycle argument %q missing: %v", label, lifecycleLabels)
		}
	}
}

func TestCompletionUsesInitializedTerraformModuleInputs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	source := `import module Bucket from "terraform-aws-modules/s3-bucket/aws"
module bucket = Bucket("storage", {
  acl
})
`
	writeTestFile(t, filepath.Join(root, ".terraform", "modules", "modules.json"), `{
  "Modules": [{
    "Key": "storage",
    "Source": "registry.terraform.io/terraform-aws-modules/s3-bucket/aws",
    "Version": "5.15.4",
    "Dir": ".terraform/modules/storage"
  }]
}`)
	writeTestFile(t, filepath.Join(root, ".terraform", "modules", "storage", "variables.tf"), `
variable "acl" {
  description = "Canned ACL to apply"
  type        = string
  default     = null
}

variable "bucket_prefix" {
  type    = string
  default = null
}

variable "bucket" {
  type = string
}
`)

	workspace := newWorkspace()
	uri := pathToURI(path)
	if _, err := workspace.open(uri, source, 1); err != nil {
		t.Fatalf("open document: %v", err)
	}
	server := &server{workspace: workspace}
	offset := strings.Index(source, "acl\n") + len("acl")
	items := server.completions(TextDocumentPositionParams{
		TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(source, offset),
	})
	labels := completionLabels(items)
	if !labels["acl"] || !labels["bucket"] || !labels["bucketPrefix"] {
		t.Fatalf("Terraform module input completions = %v", labels)
	}
	if labels["module"] || labels["output"] {
		t.Fatalf("non-input completions leaked into module argument keys: %v", labels)
	}
	for _, item := range items {
		if item.Label == "acl" && (item.Detail != "optional module input (string)" || item.Documentation != "Canned ACL to apply") {
			t.Fatalf("acl completion metadata = %#v", item)
		}
		if item.Label == "bucket" && item.Detail != "required module input (string)" {
			t.Fatalf("bucket completion metadata = %#v", item)
		}
	}
}

func TestProviderMethodsAreFilteredByDeclarationKind(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	workspace.schemas.entries[root] = &schemaCacheEntry{
		requirements: providerRequirementKey([]providerRequirement{{LocalName: "aws", Source: "hashicorp/aws"}}),
		providers: parseTerraformSchemas([]byte(`{
  "provider_schemas": {
    "registry.terraform.io/hashicorp/aws": {
      "resource_schemas": {"aws_s3_bucket": {"block": {}}},
      "data_source_schemas": {"aws_caller_identity": {"block": {}}}
    }
  }
}`)),
	}
	server := &server{workspace: workspace}

	for _, test := range []struct {
		source string
		want   string
		reject string
	}{
		{source: "provider AWS from \"hashicorp/aws\"\nconfigure aws = AWS({})\nresource value = aws.", want: "s3Bucket", reject: "callerIdentity"},
		{source: "provider AWS from \"hashicorp/aws\"\nconfigure aws = AWS({})\ndata value = aws.", want: "callerIdentity", reject: "s3Bucket"},
	} {
		uri := pathToURI(path)
		if _, err := workspace.change(uri, test.source, 1); err != nil {
			t.Fatalf("change document: %v", err)
		}
		labels := completionLabels(server.completions(TextDocumentPositionParams{
			TextDocument: TextDocumentIdentifier{URI: uri}, Position: positionAt(test.source, len(test.source)),
		}))
		if !labels[test.want] || labels[test.reject] {
			t.Fatalf("method completions for %q = %v", test.source, labels)
		}
	}
}

func TestDiagnosticsCompileUnsavedSource(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.infra")
	workspace := newWorkspace()
	uri := pathToURI(path)
	index, err := workspace.open(uri, "output value = missing.value\n", 1)
	if err != nil {
		t.Fatalf("open document: %v", err)
	}
	diagnostics := compileDiagnostics([]*fileIndex{index}, workspace)[path]
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "unknown name") {
		t.Fatalf("expected compiler diagnostic for overlay, got %#v", diagnostics)
	}
	if diagnostics[0].Source != "InfraLang" || diagnostics[0].Severity != 1 {
		t.Fatalf("unexpected diagnostic metadata: %#v", diagnostics[0])
	}
}

func TestDiagnosticsResolveSiblingInfraModuleForInputForwarding(t *testing.T) {
	root := t.TempDir()
	hostDirectory := filepath.Join(root, "modules", "host")
	childDirectory := filepath.Join(root, "modules", "child")
	writeTestFile(t, filepath.Join(hostDirectory, "types.infra"), `
export type ChildConfig = object {
  enabled?: bool = false,
}
`)
	writeTestFile(t, filepath.Join(hostDirectory, "main.infra"), `
import module Child from "../child"
input children: map<ChildConfig>
module childModules = Child("child", {
  ...inputs(each.value),
  name: each.key,
}) with { forEach: children }
`)
	writeTestFile(t, filepath.Join(childDirectory, "main.infra"), `
input enabled: bool = false
input name: string
output selected = name
`)

	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	if err := workspace.scan(); err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	diagnostics := compileDiagnostics(workspace.directoryFiles(hostDirectory), workspace)
	for path, items := range diagnostics {
		if len(items) != 0 {
			t.Fatalf("diagnostics for %s = %#v, want none", path, items)
		}
	}
}

func TestWatchedFileChangesRefreshWorkspaceIndex(t *testing.T) {
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.infra")
	secondPath := filepath.Join(root, "second.infra")
	writeTestFile(t, firstPath, "let first = true\n")
	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	if err := workspace.scan(); err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	writeTestFile(t, secondPath, "let second = true\n")
	var output bytes.Buffer
	server := &server{workspace: workspace, writer: newFramedWriter(&output)}
	params, err := json.Marshal(map[string]any{
		"changes": []map[string]any{{"uri": pathToURI(secondPath), "type": 1}},
	})
	if err != nil {
		t.Fatalf("marshal notification: %v", err)
	}
	server.handle(rpcMessage{JSONRPC: "2.0", Method: "workspace/didChangeWatchedFiles", Params: params})
	if server.visibleSymbols(root)["second"] == nil {
		t.Fatalf("created watched file was not indexed")
	}

	if err := os.Remove(secondPath); err != nil {
		t.Fatalf("remove watched file: %v", err)
	}
	params, _ = json.Marshal(map[string]any{
		"changes": []map[string]any{{"uri": pathToURI(secondPath), "type": 3}},
	})
	server.handle(rpcMessage{JSONRPC: "2.0", Method: "workspace/didChangeWatchedFiles", Params: params})
	if server.visibleSymbols(root)["second"] != nil {
		t.Fatalf("deleted watched file remained indexed")
	}
}

func TestProviderSchemaDirectoryRecognizesTerraformArtifacts(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, ".terraform.lock.hcl"),
		filepath.Join(root, ".terraform", "providers", "registry.terraform.io", "hashicorp", "aws", "6.0.0", "provider"),
		filepath.Join(root, ".terraform", "modules", "modules.json"),
	} {
		if got := providerSchemaDirectory(path); got != root {
			t.Errorf("providerSchemaDirectory(%q) = %q, want %q", path, got, root)
		}
	}
	if got := providerSchemaDirectory(filepath.Join(root, "main.infra")); got != "" {
		t.Fatalf("providerSchemaDirectory(main.infra) = %q, want empty", got)
	}
}

func TestSchemaManagerInvalidationDropsCachedAttempt(t *testing.T) {
	manager := newSchemaManager()
	directory := t.TempDir()
	manager.entries[directory] = &schemaCacheEntry{attemptedAt: time.Now()}
	manager.invalidate(directory)
	if manager.entries[directory] != nil {
		t.Fatalf("schema cache entry remained after invalidation")
	}
}

func TestProviderRequirementsAndIsolatedCacheConfiguration(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "main.infra"), `
provider AWS from "hashicorp/aws" version "~> 6.0"
provider Lvm from "github.com/ondrejnov/lvm" version "0.5.0"
provider Terraform from "terraform.io/builtin/terraform"
`)
	workspace := newWorkspace()
	workspace.setRoots([]string{root})
	if err := workspace.scan(); err != nil {
		t.Fatalf("scan workspace: %v", err)
	}
	requirements := (&server{workspace: workspace}).providerRequirements(root)
	if len(requirements) != 3 {
		t.Fatalf("provider requirements = %#v, want three providers", requirements)
	}
	if requirements[0] != (providerRequirement{LocalName: "aws", Source: "hashicorp/aws", Version: "~> 6.0"}) ||
		requirements[1] != (providerRequirement{LocalName: "lvm", Source: "github.com/ondrejnov/lvm", Version: "0.5.0"}) ||
		requirements[2] != (providerRequirement{LocalName: "terraform", Source: "terraform.io/builtin/terraform"}) {
		t.Fatalf("provider requirements = %#v", requirements)
	}

	configuration, err := providerSchemaCacheConfiguration(requirements)
	if err != nil {
		t.Fatalf("build provider cache configuration: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(configuration, &decoded); err != nil {
		t.Fatalf("decode provider cache configuration: %v", err)
	}
	terraform := decoded["terraform"].(map[string]any)
	required := terraform["required_providers"].(map[string]any)
	aws := required["aws"].(map[string]any)
	if aws["source"] != "hashicorp/aws" || aws["version"] != "~> 6.0" {
		t.Fatalf("AWS cache requirement = %#v", aws)
	}
	resource := decoded["resource"].(map[string]any)
	if resource["terraform_data"] == nil {
		t.Fatalf("built-in provider schema resource is missing: %#v", decoded)
	}

	providers := map[string]*providerSchema{
		"registry.terraform.io/hashicorp/aws": {},
		"github.com/ondrejnov/lvm":            {},
		"terraform.io/builtin/terraform":      {},
	}
	if !providerSchemasCoverRequirements(providers, requirements) {
		t.Fatalf("complete provider schemas were rejected")
	}
	delete(providers, "github.com/ondrejnov/lvm")
	if providerSchemasCoverRequirements(providers, requirements) {
		t.Fatalf("incomplete provider schemas were accepted")
	}
	if !providerSourceMatches("registry.terraform.io/hashicorp/aws", "registry.terraform.io/hashicorp/aws") {
		t.Fatalf("fully qualified registry provider sources did not match")
	}
}

func completionLabels(items []CompletionItem) map[string]bool {
	result := make(map[string]bool, len(items))
	for _, item := range items {
		result[item.Label] = true
	}
	return result
}

func writeTestFile(t *testing.T, path, source string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create test directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}
