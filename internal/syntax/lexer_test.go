package syntax

import "testing"

func TestLexFormattedStringAndOperators(t *testing.T) {
	t.Parallel()

	tokens, diagnostics := Lex("test.infra", `
// comment
let value = f"host-{name}" ?? "fallback"
`)
	if len(diagnostics) != 0 {
		t.Fatalf("Lex() diagnostics = %v", diagnostics)
	}

	wantKinds := []TokenKind{
		TokenIdentifier,
		TokenIdentifier,
		TokenAssign,
		TokenFString,
		TokenCoalesce,
		TokenString,
		TokenEOF,
	}
	if len(tokens) != len(wantKinds) {
		t.Fatalf("Lex() returned %d tokens, want %d: %#v", len(tokens), len(wantKinds), tokens)
	}
	for index, want := range wantKinds {
		if tokens[index].Kind != want {
			t.Errorf("tokens[%d].Kind = %s, want %s", index, tokens[index].Kind, want)
		}
	}
	if tokens[3].Lexeme != "host-{name}" {
		t.Errorf("formatted string = %q, want %q", tokens[3].Lexeme, "host-{name}")
	}
}

func TestLexReportsUnterminatedComment(t *testing.T) {
	t.Parallel()

	_, diagnostics := Lex("test.infra", "/* missing")
	if len(diagnostics) != 1 {
		t.Fatalf("Lex() returned %d diagnostics, want 1", len(diagnostics))
	}
	if diagnostics[0].Message != "unterminated block comment" {
		t.Errorf("diagnostic = %q", diagnostics[0].Message)
	}
}
