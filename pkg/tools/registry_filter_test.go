package tools

import "testing"

func TestToolRegistry_ToProviderDefsFiltered(t *testing.T) {
	r := NewToolRegistry()
	r.Register(newMockTool("message", "send message"))
	r.Register(newMockTool("fact_check", "fact check"))

	allowed := map[string]struct{}{
		"fact_check": {},
	}
	defs := r.ToProviderDefsFiltered(allowed)
	if len(defs) != 1 {
		t.Fatalf("len(defs) = %d, want 1", len(defs))
	}
	if defs[0].Function.Name != "fact_check" {
		t.Fatalf("defs[0].Function.Name = %q, want fact_check", defs[0].Function.Name)
	}
}
