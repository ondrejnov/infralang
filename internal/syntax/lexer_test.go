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

func TestLexPhaseOneTokens(t *testing.T) {
	t.Parallel()

	tokens, diagnostics := Lex("phase.infra", "...value `module.old[\"x\"]` -> `module.new[\"x\"]` key => value")
	if len(diagnostics) != 0 {
		t.Fatalf("Lex() diagnostics = %v", diagnostics)
	}
	want := []TokenKind{
		TokenEllipsis, TokenIdentifier, TokenRawAddress, TokenArrow,
		TokenRawAddress, TokenIdentifier, TokenFatArrow, TokenIdentifier, TokenEOF,
	}
	if len(tokens) != len(want) {
		t.Fatalf("Lex() tokens = %#v", tokens)
	}
	for index, kind := range want {
		if tokens[index].Kind != kind {
			t.Errorf("tokens[%d].Kind = %s, want %s", index, tokens[index].Kind, kind)
		}
	}
	if tokens[2].Lexeme != `module.old["x"]` {
		t.Errorf("raw address = %q", tokens[2].Lexeme)
	}
}
