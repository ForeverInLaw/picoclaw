package extract

import (
	"testing"
)

func TestParseFacts_Valid(t *testing.T) {
	raw := `{"facts":[{"entity":"Андрей","attribute":"likes","value":"грейпфрут","confidence":0.9}]}`
	out, err := ParseFacts(raw)
	if err != nil {
		t.Fatalf("ParseFacts: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1, got %d", len(out))
	}
	if out[0].Entity != "Андрей" {
		t.Fatalf("bad entity: %q", out[0].Entity)
	}
	if out[0].Confidence != 0.9 {
		t.Fatalf("bad confidence: %v", out[0].Confidence)
	}
}

func TestParseFacts_Malformed(t *testing.T) {
	if _, err := ParseFacts("not json"); err == nil {
		t.Fatal("want error on malformed json")
	}
}

func TestParseFacts_StripsFencing(t *testing.T) {
	raw := "```json\n{\"facts\":[]}\n```"
	out, err := ParseFacts(raw)
	if err != nil {
		t.Fatalf("ParseFacts: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("want 0, got %d", len(out))
	}
}
