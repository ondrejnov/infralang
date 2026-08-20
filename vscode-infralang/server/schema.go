package main

import (
	"context"
	"encoding/json"
	"os/exec"
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
	loading     bool
	attemptedAt time.Time
	done        chan struct{}
	providers   map[string]*providerSchema
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

func (manager *schemaManager) ensure(directory string) {
	manager.mu.Lock()
	if !manager.enabled {
		manager.mu.Unlock()
		return
	}
	entry := manager.entries[directory]
	if entry == nil {
		entry = &schemaCacheEntry{}
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var providers map[string]*providerSchema
		for _, executable := range []string{"terraform", "tofu"} {
			if _, err := exec.LookPath(executable); err != nil {
				continue
			}
			command := exec.CommandContext(ctx, executable, "providers", "schema", "-json")
			command.Dir = directory
			output, err := command.Output()
			if err == nil {
				providers = parseTerraformSchemas(output)
				break
			}
		}
		manager.mu.Lock()
		entry.loading = false
		entry.providers = providers
		close(entry.done)
		manager.mu.Unlock()
	}()
}

func (manager *schemaManager) await(directory string) {
	manager.ensure(directory)
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

func (manager *schemaManager) provider(directory, source string) *providerSchema {
	manager.await(directory)
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	entry := manager.entries[directory]
	if entry == nil {
		return nil
	}
	for key, provider := range entry.providers {
		normalized := strings.TrimPrefix(key, "registry.terraform.io/")
		if normalized == source || strings.HasSuffix(normalized, "/"+source) {
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
