package compat

import "testing"

func TestTokenizeIsUnicodeAndCaseStable(t *testing.T) {
	tokens := tokenize("ÁRBOL, árbol! Go-123")
	want := []string{"árbol", "árbol", "go", "123"}
	if len(tokens) != len(want) {
		t.Fatalf("unexpected tokens %#v", tokens)
	}
	for i := range want {
		if tokens[i] != want[i] {
			t.Fatalf("unexpected tokens %#v", tokens)
		}
	}
}
