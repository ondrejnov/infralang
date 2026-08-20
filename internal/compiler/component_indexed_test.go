package compiler

import (
	"strings"
	"testing"
)

func TestCompileComponentExportResolvesInternalIndexedHandle(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
component Indexed() {
  module children["key"] "child" from "registry.example/child" {}
  export id = children["key"].id
}
instantiate indexed = Indexed()
output id = indexed.id
`)
	if !strings.Contains(string(result), `${module.child.id}`) {
		t.Fatalf("component indexed export:\n%s", result)
	}
}

func TestCompileStaticLoopCanInstantiateIndexedComponents(t *testing.T) {
	t.Parallel()

	result := compileSource(t, `
component Child(label: string) {
  module child label from "registry.example/child" {}
  export id = child.id
}
static for label in ["first", "second"] {
  instantiate children[label] = Child(label: label)
}
output first = children["first"].id
output second = children["second"].id
`)
	text := string(result)
	for _, expected := range []string{`${module.first.id}`, `${module.second.id}`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("static component expansion missing %q:\n%s", expected, result)
		}
	}
}
