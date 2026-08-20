package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/ondrejnov/infralang/internal/compiler"
)

type terraformProviderSchemaDocument struct {
	ProviderSchemas map[string]struct {
		Provider struct {
			Block struct {
				BlockTypes map[string]struct {
					NestingMode string `json:"nesting_mode"`
				} `json:"block_types"`
			} `json:"block"`
		} `json:"provider"`
	} `json:"provider_schemas"`
}

func loadProviderSchemas(root string) (compiler.ProviderSchemas, error) {
	command := exec.Command("terraform", "providers", "schema", "-json")
	command.Dir = root
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if stderr.Len() == 0 {
			return nil, err
		}
		return nil, fmt.Errorf("terraform providers schema: %s", bytes.TrimSpace(stderr.Bytes()))
	}

	var document terraformProviderSchemaDocument
	if err := json.Unmarshal(output, &document); err != nil {
		return nil, fmt.Errorf("decode Terraform provider schema: %w", err)
	}
	result := make(compiler.ProviderSchemas, len(document.ProviderSchemas))
	for source, provider := range document.ProviderSchemas {
		blocks := make(map[string]compiler.ProviderBlockSchema, len(provider.Provider.Block.BlockTypes))
		for name, block := range provider.Provider.Block.BlockTypes {
			blocks[name] = compiler.ProviderBlockSchema{NestingMode: block.NestingMode}
		}
		result[source] = compiler.ProviderSchema{BlockTypes: blocks}
	}
	return result, nil
}
