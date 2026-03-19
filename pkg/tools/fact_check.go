package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/sipeed/picoclaw/pkg/factcheck"
)

type FactCheckTool struct {
	service *factcheck.Service
}

func NewFactCheckTool(service *factcheck.Service) *FactCheckTool {
	return &FactCheckTool{service: service}
}

func (t *FactCheckTool) Name() string {
	return "fact_check"
}

func (t *FactCheckTool) Description() string {
	return "Checks a factual claim or URL against web sources and returns a verdict with citations. Use this when the user explicitly asks to verify, fact-check, or check whether something is true."
}

func (t *FactCheckTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"claim": map[string]any{
				"type":        "string",
				"description": "Claim to verify",
			},
			"url": map[string]any{
				"type":        "string",
				"description": "Optional URL whose main claim should be verified",
			},
			"question": map[string]any{
				"type":        "string",
				"description": "Optional framing question or angle for the verification",
			},
		},
	}
}

func (t *FactCheckTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	if t == nil || t.service == nil {
		return ErrorResult("fact check service is not configured")
	}
	req := factcheck.Request{
		Claim:    strings.TrimSpace(asString(args["claim"])),
		URL:      strings.TrimSpace(asString(args["url"])),
		Question: strings.TrimSpace(asString(args["question"])),
	}
	if req.Claim == "" && req.URL == "" {
		return ErrorResult("claim or url is required")
	}
	result, err := t.service.Check(ctx, req)
	if err != nil {
		return ErrorResult(fmt.Sprintf("fact check failed: %v", err)).WithError(err)
	}
	return SilentResult(renderFactCheckResult(result))
}

func renderFactCheckResult(result factcheck.Result) string {
	var b strings.Builder
	b.WriteString("FACT_CHECK\n")
	fmt.Fprintf(&b, "verdict=%s\n", result.Verdict)
	fmt.Fprintf(&b, "claim=%s\n", strings.TrimSpace(result.Claim))
	if rationale := strings.TrimSpace(result.Rationale); rationale != "" {
		fmt.Fprintf(&b, "rationale=%s\n", rationale)
	}
	if len(result.Sources) > 0 {
		b.WriteString("sources:\n")
		for i, source := range result.Sources {
			fmt.Fprintf(&b, "%d. [%s] %s\n   %s\n", i+1, source.SourceType, strings.TrimSpace(source.Title), strings.TrimSpace(source.URL))
		}
	}
	return strings.TrimSpace(b.String())
}
