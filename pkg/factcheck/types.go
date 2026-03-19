package factcheck

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/websource"
)

type Verdict string

const (
	VerdictSupported    Verdict = "supported"
	VerdictContradicted Verdict = "contradicted"
	VerdictMixed        Verdict = "mixed"
	VerdictUnverified   Verdict = "unverified"
)

type Source struct {
	Title      string
	URL        string
	SourceType string
}

type Result struct {
	Verdict   Verdict
	Claim     string
	Rationale string
	Sources   []Source
}

type Request struct {
	Claim    string
	URL      string
	Question string
}

type Searcher interface {
	Search(ctx context.Context, query string, count int) ([]websource.SearchHit, error)
}

type Fetcher interface {
	Fetch(ctx context.Context, url string, maxChars int) (*websource.Document, error)
}
