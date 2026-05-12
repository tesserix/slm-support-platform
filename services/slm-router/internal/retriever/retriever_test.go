package retriever

import "testing"

func TestVectorLiteral(t *testing.T) {
	got := vectorLiteral([]float32{1, 2.5, -0.25})
	want := "[1,2.5,-0.25]"
	if got != want {
		t.Fatalf("vectorLiteral: got %q want %q", got, want)
	}
}

func TestVectorLiteralEmpty(t *testing.T) {
	got := vectorLiteral(nil)
	if got != "[]" {
		t.Fatalf("vectorLiteral(nil): got %q want []", got)
	}
}
