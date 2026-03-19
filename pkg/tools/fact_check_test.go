package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/factcheck"
	"github.com/sipeed/picoclaw/pkg/websource"
)

type factCheckSearcher struct{}

func (factCheckSearcher) Search(_ context.Context, _ string, _ int) ([]websource.SearchHit, error) {
	return []websource.SearchHit{
		{Title: "Official post", URL: "https://example.com/post", Snippet: "ExampleCorp confirmed the launch."},
		{Title: "Coverage", URL: "https://news.example.net/post", Snippet: "Independent coverage confirmed the launch."},
	}, nil
}

type factCheckFetcher struct{}

func (factCheckFetcher) Fetch(_ context.Context, rawURL string, _ int) (*websource.Document, error) {
	switch rawURL {
	case "https://example.com/post":
		return &websource.Document{Title: "Official post", Text: "ExampleCorp launched the product and officially confirmed the launch."}, nil
	case "https://news.example.net/post":
		return &websource.Document{Title: "Coverage", Text: "Coverage reported ExampleCorp launched the product and confirmed the release."}, nil
	default:
		return &websource.Document{Title: rawURL, Text: ""}, nil
	}
}

func TestFactCheckTool_Description_EncouragesExplicitVerification(t *testing.T) {
	tool := NewFactCheckTool(factcheck.NewService(factCheckSearcher{}, factCheckFetcher{}))
	desc := tool.Description()
	for _, needle := range []string{"verify", "fact-check", "true"} {
		if !strings.Contains(desc, needle) {
			t.Fatalf("description missing %q: %s", needle, desc)
		}
	}
}

func TestFactCheckTool_ExecuteRequiresClaimOrURL(t *testing.T) {
	tool := NewFactCheckTool(factcheck.NewService(factCheckSearcher{}, factCheckFetcher{}))
	result := tool.Execute(context.Background(), map[string]any{})
	if !result.IsError {
		t.Fatal("expected error for empty request")
	}
}

func TestFactCheckTool_ExecuteFormatsVerdict(t *testing.T) {
	tool := NewFactCheckTool(factcheck.NewService(factCheckSearcher{}, factCheckFetcher{}))
	result := tool.Execute(context.Background(), map[string]any{"claim": "ExampleCorp launched the product"})
	if result.IsError {
		t.Fatalf("Execute() error = %s", result.ForLLM)
	}
	for _, needle := range []string{"FACT_CHECK", "verdict=supported", "sources:"} {
		if !strings.Contains(result.ForLLM, needle) {
			t.Fatalf("result missing %q:\n%s", needle, result.ForLLM)
		}
	}
}
