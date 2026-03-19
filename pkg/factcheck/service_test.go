package factcheck

import (
	"context"
	"testing"

	"github.com/sipeed/picoclaw/pkg/websource"
)

type fakeSearcher struct {
	hits []websource.SearchHit
}

func (f fakeSearcher) Search(_ context.Context, _ string, _ int) ([]websource.SearchHit, error) {
	return f.hits, nil
}

type fakeFetcher struct {
	docs map[string]*websource.Document
}

func (f fakeFetcher) Fetch(_ context.Context, rawURL string, _ int) (*websource.Document, error) {
	return f.docs[rawURL], nil
}

func TestServiceCheck_Supported(t *testing.T) {
	service := NewService(
		fakeSearcher{hits: []websource.SearchHit{
			{Title: "Press release", URL: "https://example.com/news", Snippet: "ExampleCorp announced the release officially."},
			{Title: "Coverage", URL: "https://news.example.net/item", Snippet: "Several outlets reported the same release."},
		}},
		fakeFetcher{docs: map[string]*websource.Document{
			"https://example.com/news":      {Title: "ExampleCorp announced product launch", Text: "ExampleCorp announced product launch officially and confirmed the release."},
			"https://news.example.net/item": {Title: "Coverage", Text: "Media reported ExampleCorp announced product launch and confirmed the release."},
		}},
	)

	result, err := service.Check(context.Background(), Request{Claim: "ExampleCorp announced product launch"})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if result.Verdict != VerdictSupported {
		t.Fatalf("verdict = %s, want %s", result.Verdict, VerdictSupported)
	}
	if len(result.Sources) == 0 {
		t.Fatal("expected citations")
	}
}

func TestServiceCheck_Contradicted(t *testing.T) {
	service := NewService(
		fakeSearcher{hits: []websource.SearchHit{
			{Title: "Official denial", URL: "https://agency.gov/notice", Snippet: "The agency says the claim is false."},
			{Title: "Debunk", URL: "https://news.example.net/debunk", Snippet: "The rumor was debunked as incorrect."},
		}},
		fakeFetcher{docs: map[string]*websource.Document{
			"https://agency.gov/notice":       {Title: "Official notice", Text: "The claim that ExampleCorp was fined is false and incorrect."},
			"https://news.example.net/debunk": {Title: "Debunk", Text: "Reporters said the claim ExampleCorp was fined is false and debunked."},
		}},
	)

	result, err := service.Check(context.Background(), Request{Claim: "ExampleCorp was fined"})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if result.Verdict != VerdictContradicted {
		t.Fatalf("verdict = %s, want %s", result.Verdict, VerdictContradicted)
	}
}

func TestServiceCheck_Unverified(t *testing.T) {
	service := NewService(
		fakeSearcher{hits: []websource.SearchHit{
			{Title: "Mention", URL: "https://blog.example.com/post", Snippet: "Some mentions exist."},
		}},
		fakeFetcher{docs: map[string]*websource.Document{
			"https://blog.example.com/post": {Title: "Mention", Text: "This post mentions ExampleCorp and satellites but does not confirm the claim."},
		}},
	)

	result, err := service.Check(context.Background(), Request{Claim: "ExampleCorp launched a satellite"})
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if result.Verdict != VerdictUnverified {
		t.Fatalf("verdict = %s, want %s", result.Verdict, VerdictUnverified)
	}
}
