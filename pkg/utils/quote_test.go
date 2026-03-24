package utils

import "testing"

func TestQuoteBlock(t *testing.T) {
	got := QuoteBlock("  first line \n\n second line  ")
	want := "> first line\n>\n> second line"
	if got != want {
		t.Fatalf("QuoteBlock() = %q, want %q", got, want)
	}
}

func TestFormatQuotedMessage(t *testing.T) {
	got := FormatQuotedMessage("what is this?", "Thinking...")
	want := "> what is this?\n\nThinking..."
	if got != want {
		t.Fatalf("FormatQuotedMessage() = %q, want %q", got, want)
	}
}
