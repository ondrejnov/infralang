package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ondrejnov/infralang/internal/syntax"
)

type schemaField struct {
	Name        string
	WireName    string
	Description string
	Required    bool
	Optional    bool
	Computed    bool
	Block       *schemaBlock
}

type schemaBlock struct {
	Fields map[string]schemaField
}

func resourceMetaSchema() *schemaBlock {
	return &schemaBlock{Fields: map[string]schemaField{
		"count": {
			Name: "count", WireName: "count",
		},
		"forEach": {
			Name: "forEach", WireName: "for_each",
		},
		"dependsOn": {
			Name: "dependsOn", WireName: "depends_on",
		},
		"lifecycle": {
			Name: "lifecycle", WireName: "lifecycle",
			Block: &schemaBlock{Fields: map[string]schemaField{
				"createBeforeDestroy": {
					Name: "createBeforeDestroy", WireName: "create_before_destroy",
				},
				"preventDestroy": {
					Name: "preventDestroy", WireName: "prevent_destroy",
				},
				"ignoreChanges": {
					Name: "ignoreChanges", WireName: "ignore_changes",
				},
				"replaceTriggeredBy": {
					Name: "replaceTriggeredBy", WireName: "replace_triggered_by",
				},
			}},
		},
	}}
}

type providerSchema struct {
	Provider  *schemaBlock
	Resources map[string]*schemaBlock
	Data      map[string]*schemaBlock
}

type schemaCacheEntry struct {
	loading      bool
	attemptedAt  time.Time
	done         chan struct{}
	providers    map[string]*providerSchema
	requirements string
}

type providerRequirement struct {
	LocalName string
	Source    string
	Version   string
}

type schemaManager struct {
	mu      sync.RWMutex
	entries map[string]*schemaCacheEntry
	enabled bool
}

func newSchemaManager() *schemaManager {
	return &schemaManager{entries: make(map[string]*schemaCacheEntry), enabled: true}
}

func (manager *schemaManager) setEnabled(enabled bool) {
	manager.mu.Lock()
	manager.enabled = enabled
	manager.mu.Unlock()
}

func (manager *schemaManager) invalidate(directory string) {
	manager.mu.Lock()
	delete(manager.entries, directory)
	manager.mu.Unlock()
}

func (manager *schemaManager) ensure(directory string, requirements []providerRequirement) {
	requirementKey := providerRequirementKey(requirements)
	manager.mu.Lock()
	if !manager.enabled {
		manager.mu.Unlock()
		return
	}
	entry := manager.entries[directory]
	if entry == nil || entry.requirements != requirementKey {
		entry = &schemaCacheEntry{requirements: requirementKey}
		manager.entries[directory] = entry
	}
	if entry.loading || entry.providers != nil || (!entry.attemptedAt.IsZero() && time.Since(entry.attemptedAt) < 30*time.Second) {
		manager.mu.Unlock()
		return
	}
	entry.loading = true
	entry.attemptedAt = time.Now()
	entry.done = make(chan struct{})
	manager.mu.Unlock()

	go func() {
		providers := loadProviderSchemas(directory, requirements, requirementKey)
		manager.mu.Lock()
		entry.loading = false
		entry.providers = providers
		close(entry.done)
		manager.mu.Unlock()
	}()
}

func (manager *schemaManager) await(directory string, requirements []providerRequirement) {
	manager.ensure(directory, requirements)
	manager.mu.RLock()
	entry := manager.entries[directory]
	if entry == nil || !entry.loading {
		manager.mu.RUnlock()
		return
	}
	done := entry.done
	manager.mu.RUnlock()
	<-done
}

func (manager *schemaManager) provider(directory, source string, requirements []providerRequirement) *providerSchema {
	manager.await(directory, requirements)
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	entry := manager.entries[directory]
	if entry == nil {
		return nil
	}
	for key, provider := range entry.providers {
		if providerSourceMatches(key, source) {
			return provider
		}
	}
	wanted := providerLocalName(source)
	for key, provider := range entry.providers {
		if providerLocalName(key) == wanted {
			return provider
		}
	}
	return nil
}

func loadProviderSchemas(directory string, requirements []providerRequirement, requirementKey string) map[string]*providerSchema {
	for _, executable := range []string{"terraform", "tofu"} {
		if _, err := exec.LookPath(executable); err != nil {
			fmt.Fprintf(os.Stderr, "infralang-lsp: %q not found in PATH\n", executable)
			continue
		}
		fmt.Fprintf(os.Stderr, "infralang-lsp: loading provider schemas via %q in %s\n", executable, directory)
		providers, err := runProviderSchemaCommand(executable, directory)
		if err == nil && providerSchemasCoverRequirements(providers, requirements) {
			fmt.Fprintf(os.Stderr, "infralang-lsp: loaded %d provider schemas from project\n", len(providers))
			return providers
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "infralang-lsp: %q providers schema failed: %v\n", executable, err)
		}
		if len(requirements) == 0 {
			continue
		}
		providers, err = loadCachedProviderSchemas(executable, requirements, requirementKey)
		if err == nil && providerSchemasCoverRequirements(providers, requirements) {
			fmt.Fprintf(os.Stderr, "infralang-lsp: loaded %d provider schemas from isolated cache\n", len(providers))
			return providers
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "infralang-lsp: cached provider schema load via %q failed: %v\n", executable, err)
		}
	}
	return nil
}

func runProviderSchemaCommand(executable, directory string) (map[string]*providerSchema, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "providers", "schema", "-json")
	command.Dir = directory
	command.Env = append(os.Environ(), "TF_IN_AUTOMATION=1")
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	return parseTerraformSchemas(output), nil
}

func loadCachedProviderSchemas(executable string, requirements []providerRequirement, requirementKey string) (map[string]*providerSchema, error) {
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(cacheRoot, "infralang", "provider-schemas", executable+"-"+requirementKey)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, err
	}
	configuration, err := providerSchemaCacheConfiguration(requirements)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(directory, "main.tf.json"), configuration, 0o644); err != nil {
		return nil, err
	}
	if providers, err := runProviderSchemaCommand(executable, directory); err == nil && providerSchemasCoverRequirements(providers, requirements) {
		return providers, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "init", "-backend=false", "-input=false", "-no-color")
	command.Dir = directory
	command.Env = append(os.Environ(), "TF_IN_AUTOMATION=1")
	if output, err := command.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("init isolated provider cache: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return runProviderSchemaCommand(executable, directory)
}

func providerSchemaCacheConfiguration(requirements []providerRequirement) ([]byte, error) {
	required := make(map[string]any, len(requirements))
	configuration := make(map[string]any)
	for _, requirement := range requirements {
		if requirement.Source == "terraform.io/builtin/terraform" {
			configuration["resource"] = map[string]any{
				"terraform_data": map[string]any{"infralang_schema": map[string]any{}},
			}
			continue
		}
		provider := map[string]any{"source": requirement.Source}
		if requirement.Version != "" {
			provider["version"] = requirement.Version
		}
		required[requirement.LocalName] = provider
	}
	if len(required) > 0 {
		configuration["terraform"] = map[string]any{"required_providers": required}
	}
	result, err := json.MarshalIndent(configuration, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(result, '\n'), nil
}

func providerRequirementKey(requirements []providerRequirement) string {
	hash := sha256.New()
	for _, requirement := range requirements {
		fmt.Fprintf(hash, "%s\x00%s\x00%s\x00", requirement.LocalName, requirement.Source, requirement.Version)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))[:16]
}

func providerSchemasCoverRequirements(providers map[string]*providerSchema, requirements []providerRequirement) bool {
	for _, requirement := range requirements {
		found := false
		for source := range providers {
			if providerSourceMatches(source, requirement.Source) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func providerSourceMatches(schemaSource, declaredSource string) bool {
	schemaSource = strings.TrimPrefix(schemaSource, "registry.terraform.io/")
	declaredSource = strings.TrimPrefix(declaredSource, "registry.terraform.io/")
	return schemaSource == declaredSource || strings.HasSuffix(schemaSource, "/"+declaredSource)
}

func providerLocalName(source string) string {
	source = strings.TrimSuffix(source, "/")
	if index := strings.LastIndexByte(source, '/'); index >= 0 {
		source = source[index+1:]
	}
	return source
}

func parseTerraformSchemas(data []byte) map[string]*providerSchema {
	var document struct {
		ProviderSchemas map[string]struct {
			Provider          rawSchema            `json:"provider"`
			ResourceSchemas   map[string]rawSchema `json:"resource_schemas"`
			DataSourceSchemas map[string]rawSchema `json:"data_source_schemas"`
		} `json:"provider_schemas"`
	}
	if json.Unmarshal(data, &document) != nil {
		return nil
	}
	result := make(map[string]*providerSchema)
	for source, rawProvider := range document.ProviderSchemas {
		provider := &providerSchema{
			Provider:  convertSchemaBlock(rawProvider.Provider.Block),
			Resources: make(map[string]*schemaBlock), Data: make(map[string]*schemaBlock),
		}
		for name, schema := range rawProvider.ResourceSchemas {
			provider.Resources[name] = convertSchemaBlock(schema.Block)
		}
		for name, schema := range rawProvider.DataSourceSchemas {
			provider.Data[name] = convertSchemaBlock(schema.Block)
		}
		result[source] = provider
	}
	return result
}

type rawSchema struct {
	Block rawSchemaBlock `json:"block"`
}

type rawSchemaBlock struct {
	Attributes map[string]struct {
		Description string `json:"description"`
		Required    bool   `json:"required"`
		Optional    bool   `json:"optional"`
		Computed    bool   `json:"computed"`
	} `json:"attributes"`
	BlockTypes map[string]struct {
		Block rawSchemaBlock `json:"block"`
	} `json:"block_types"`
}

func convertSchemaBlock(raw rawSchemaBlock) *schemaBlock {
	block := &schemaBlock{Fields: make(map[string]schemaField)}
	for wireName, attribute := range raw.Attributes {
		name := wireToSourceName(wireName)
		block.Fields[name] = schemaField{
			Name: name, WireName: wireName, Description: attribute.Description,
			Required: attribute.Required, Optional: attribute.Optional, Computed: attribute.Computed,
		}
	}
	for wireName, nested := range raw.BlockTypes {
		name := wireToSourceName(wireName)
		block.Fields[name] = schemaField{Name: name, WireName: wireName, Block: convertSchemaBlock(nested.Block)}
	}
	return block
}

func wireToSourceName(wire string) string {
	parts := strings.Split(wire, "_")
	if len(parts) == 0 {
		return wire
	}
	var result strings.Builder
	result.WriteString(parts[0])
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		result.WriteString(strings.ToUpper(part[:1]))
		result.WriteString(part[1:])
	}
	candidate := result.String()
	if syntax.SourceNameToWire(candidate) == wire {
		return candidate
	}
	return wire
}

func schemaCompletionItems(block *schemaBlock, writable bool) []CompletionItem {
	if block == nil {
		return nil
	}
	names := make([]string, 0, len(block.Fields))
	for name := range block.Fields {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]CompletionItem, 0, len(names))
	for _, name := range names {
		field := block.Fields[name]
		if writable && field.Computed && !field.Optional && !field.Required {
			continue
		}
		label, insertText := field.Name, field.Name
		if syntax.SourceNameToWire(field.Name) != field.WireName {
			label = `"` + field.WireName + `"`
			insertText = label
		}
		detail := "provider attribute"
		if field.Block != nil {
			detail = "provider nested block"
		} else if field.Required {
			detail += " (required)"
		} else if field.Computed {
			detail += " (computed)"
		}
		items = append(items, CompletionItem{
			Label: label, InsertText: insertText, Kind: symbolKindProperty, Detail: detail,
			Documentation: field.Description, SortText: "1_" + label,
		})
	}
	return items
}
