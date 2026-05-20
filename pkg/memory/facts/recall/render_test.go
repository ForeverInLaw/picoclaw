package recall

import (
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/memory/facts"
)

func TestRender_EmptyReturnsEmpty(t *testing.T) {
	if Render(nil) != "" {
		t.Fatal("want empty for nil")
	}
}

func TestRender_FormatsList(t *testing.T) {
	out := Render([]facts.RecallHit{
		{Fact: facts.Fact{Entity: "Андрей", Attribute: "likes", Value: "грейпфрут"}},
		{Fact: facts.Fact{Entity: "Андрей", Attribute: "lives_in", Value: "Минск"}},
	})
	if !strings.Contains(out, "Известно:") {
		t.Fatalf("missing header: %q", out)
	}
	if !strings.Contains(out, "грейпфрут") {
		t.Fatalf("missing fact: %q", out)
	}
}
