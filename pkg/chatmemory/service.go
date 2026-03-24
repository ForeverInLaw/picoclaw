package chatmemory

import (
	"context"
	"fmt"
	"log"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/config"
	"github.com/sipeed/picoclaw/pkg/memoryindex"
)

type SummaryRequest struct {
	RequesterID    string
	CurrentChannel string
	CurrentChatID  string
	CurrentPeer    string
	Target         string
	SinceHours     int
	UntilHours     int
	Query          string
}

type SearchRequest struct {
	RequesterID    string
	CurrentChannel string
	CurrentChatID  string
	CurrentPeer    string
	Target         string
	Query          string
	SinceHours     int
	UntilHours     int
	Limit          int
}

type Service struct {
	index              *memoryindex.Index
	embedder           *Embedder
	cfg                config.MemoryIndexConfig
	now                func() time.Time
	backfillRetryDelay time.Duration
}

func New(index *memoryindex.Index, cfg config.MemoryIndexConfig, embedder *Embedder) *Service {
	service := &Service{
		index:              index,
		embedder:           embedder,
		cfg:                cfg,
		now:                time.Now,
		backfillRetryDelay: 30 * time.Second,
	}
	if cfg.Embeddings.StartupBackfillEnabled {
		service.startStartupBackfill()
	}
	return service
}

func (s *Service) Search(ctx context.Context, req SearchRequest) ([]memoryindex.Hit, error) {
	if s == nil || s.index == nil {
		return nil, nil
	}

	scopes, err := s.resolveScopes(ctx, req.RequesterID, req.CurrentChannel, req.CurrentChatID, req.CurrentPeer, req.Target)
	if err != nil {
		return nil, err
	}
	window := s.resolveWindow(req.SinceHours, req.UntilHours)
	limit := resolveSearchLimit(req.Limit, s.cfg.MaxResults)
	lexicalHits, err := s.index.Search(ctx, memoryindex.SearchRequest{
		Query:      req.Query,
		Channel:    req.CurrentChannel,
		ChatID:     req.CurrentChatID,
		ChatScopes: scopes,
		Since:      window.Start,
		Until:      window.End,
		Limit:      limit,
	})
	if err != nil {
		return nil, err
	}
	if s.embedder == nil || strings.TrimSpace(req.Query) == "" {
		return lexicalHits, nil
	}

	_ = s.ensureEmbeddings(ctx)
	queryVec, err := s.embedder.EmbedQuery(ctx, req.Query)
	if err != nil || len(queryVec) == 0 {
		return lexicalHits, nil
	}

	embeddingRows, err := s.index.LoadObservationEmbeddings(ctx, scopes, s.embedder.ModelName(), window.Start, window.End, 512)
	if err != nil || len(embeddingRows) == 0 {
		return lexicalHits, nil
	}

	type scoredHit struct {
		hit   memoryindex.Hit
		score float64
	}
	scored := make([]scoredHit, 0, len(embeddingRows))
	for _, row := range embeddingRows {
		score := cosineSimilarity(queryVec, row.Vector)
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredHit{hit: row.Hit, score: score})
	}
	slices.SortFunc(scored, func(a, b scoredHit) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		case a.hit.CreatedAt.After(b.hit.CreatedAt):
			return -1
		case a.hit.CreatedAt.Before(b.hit.CreatedAt):
			return 1
		default:
			return 0
		}
	})

	merged := make([]memoryindex.Hit, 0, limit)
	seen := make(map[string]struct{})
	for _, hit := range lexicalHits {
		key := hitKey(hit)
		seen[key] = struct{}{}
		merged = append(merged, hit)
	}
	for _, candidate := range scored {
		if len(merged) >= limit {
			break
		}
		key := hitKey(candidate.hit)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, candidate.hit)
	}
	return merged, nil
}

func resolveSearchLimit(requested, fallback int) int {
	if requested > 0 {
		return requested
	}
	if fallback > 0 {
		return fallback
	}
	return 5
}

func (s *Service) Summarize(ctx context.Context, req SummaryRequest) (string, error) {
	if s == nil || s.index == nil {
		return "", nil
	}

	scope, err := s.resolveTarget(ctx, req.RequesterID, req.CurrentChannel, req.CurrentChatID, req.CurrentPeer, req.Target)
	if err != nil {
		return "", err
	}
	window := s.resolveWindow(req.SinceHours, req.UntilHours)
	if err := s.ensureRollups(ctx, scope, window); err != nil {
		return "", err
	}

	rollups, err := s.index.ListChatRollups(ctx, scope, window)
	if err != nil {
		return "", err
	}
	participants, _ := s.index.ListChatParticipants(ctx, scope)

	var searchHits []memoryindex.Hit
	if strings.TrimSpace(req.Query) != "" {
		searchHits, _ = s.Search(ctx, SearchRequest{
			RequesterID:    req.RequesterID,
			CurrentChannel: req.CurrentChannel,
			CurrentChatID:  req.CurrentChatID,
			CurrentPeer:    req.CurrentPeer,
			Target:         req.Target,
			Query:          req.Query,
			SinceHours:     req.SinceHours,
			UntilHours:     req.UntilHours,
			Limit:          8,
		})
	}

	var sb strings.Builder
	sb.WriteString("CHAT_MEMORY_SUMMARY\n")
	fmt.Fprintf(&sb, "target: %s (%s:%s)\n", firstNonEmpty(scope.Label, scope.ChatID), scope.Channel, scope.ChatID)
	fmt.Fprintf(&sb, "window: %s .. %s\n", formatTime(window.Start), formatTime(window.End))
	if len(participants) > 0 {
		labels := make([]string, 0, len(participants))
		for _, participant := range participants {
			labels = append(labels, firstNonEmpty(participant.Label, participant.SenderID))
		}
		sb.WriteString("participants: ")
		sb.WriteString(strings.Join(labels, ", "))
		sb.WriteString("\n")
	}
	if len(rollups) == 0 {
		sb.WriteString("\nNo indexed messages were found in this window.\n")
		return sb.String(), nil
	}

	sb.WriteString("\nHourly rollups:\n")
	for _, rollup := range rollups {
		fmt.Fprintf(&sb, "- [%s .. %s] %s\n", formatTime(rollup.WindowStart), formatTime(rollup.WindowEnd), strings.TrimSpace(rollup.Summary))
	}
	if len(searchHits) > 0 {
		sb.WriteString("\nRelevant excerpts:\n")
		for _, hit := range searchHits {
			fmt.Fprintf(&sb, "- [%s] %s\n", formatTime(hit.CreatedAt), strings.TrimSpace(hit.Content))
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

func (s *Service) resolveScopes(
	ctx context.Context,
	requesterID, currentChannel, currentChatID, currentPeer, target string,
) ([]memoryindex.ChatScope, error) {
	if currentPeer != "direct" {
		return []memoryindex.ChatScope{{Channel: currentChannel, ChatID: currentChatID}}, nil
	}
	if strings.TrimSpace(target) != "" {
		scope, err := s.resolveTarget(ctx, requesterID, currentChannel, currentChatID, currentPeer, target)
		if err != nil {
			return nil, err
		}
		return []memoryindex.ChatScope{scope}, nil
	}

	accessible, err := s.index.ListAccessibleChats(ctx, requesterID)
	if err != nil {
		return nil, err
	}
	scopes := make([]memoryindex.ChatScope, 0, len(accessible)+1)
	seen := map[string]struct{}{}
	addScope := func(scope memoryindex.ChatScope) {
		key := scope.Channel + "\x00" + scope.ChatID
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		scopes = append(scopes, scope)
	}
	addScope(memoryindex.ChatScope{Channel: currentChannel, ChatID: currentChatID})
	for _, entry := range accessible {
		addScope(memoryindex.ChatScope{
			Channel:  entry.Channel,
			ChatID:   entry.ChatID,
			PeerKind: entry.PeerKind,
			Label:    firstNonEmpty(entry.Label, entry.Alias),
		})
	}
	return scopes, nil
}

func (s *Service) resolveTarget(
	ctx context.Context,
	requesterID, currentChannel, currentChatID, currentPeer, target string,
) (memoryindex.ChatScope, error) {
	target = strings.TrimSpace(target)
	if target == "" || strings.EqualFold(target, "current") || strings.EqualFold(target, "this chat") {
		return memoryindex.ChatScope{Channel: currentChannel, ChatID: currentChatID, PeerKind: currentPeer}, nil
	}

	accessible, err := s.index.ListAccessibleChats(ctx, requesterID)
	if err != nil {
		return memoryindex.ChatScope{}, err
	}
	normalized := normalizeLabel(target)
	var exact *memoryindex.ChatAccessEntry
	partial := make([]memoryindex.ChatAccessEntry, 0)
	for _, entry := range accessible {
		if normalizeLabel(entry.Alias) == normalized || normalizeLabel(entry.Label) == normalized {
			e := entry
			exact = &e
			break
		}
		if strings.Contains(normalizeLabel(entry.Label), normalized) || strings.Contains(normalizeLabel(entry.Alias), normalized) {
			partial = append(partial, entry)
		}
	}
	if exact != nil {
		return memoryindex.ChatScope{
			Channel:  exact.Channel,
			ChatID:   exact.ChatID,
			PeerKind: exact.PeerKind,
			Label:    firstNonEmpty(exact.Label, exact.Alias),
		}, nil
	}
	if len(partial) == 1 {
		entry := partial[0]
		return memoryindex.ChatScope{
			Channel:  entry.Channel,
			ChatID:   entry.ChatID,
			PeerKind: entry.PeerKind,
			Label:    firstNonEmpty(entry.Label, entry.Alias),
		}, nil
	}
	return memoryindex.ChatScope{}, fmt.Errorf("chat target %q is not accessible or is ambiguous", target)
}

func (s *Service) resolveWindow(sinceHours, untilHours int) memoryindex.TimeWindow {
	now := s.now().UTC()
	if sinceHours <= 0 {
		sinceHours = 24
	}
	if untilHours < 0 {
		untilHours = 0
	}
	end := now.Add(-time.Duration(untilHours) * time.Hour)
	start := end.Add(-time.Duration(sinceHours) * time.Hour)
	return memoryindex.TimeWindow{Start: start, End: end}
}

func (s *Service) ensureRollups(ctx context.Context, scope memoryindex.ChatScope, window memoryindex.TimeWindow) error {
	if !s.cfg.Rollups.Enabled {
		return nil
	}
	for slot := floorHour(window.Start); slot.Before(window.End); slot = slot.Add(time.Hour) {
		slotEnd := slot.Add(time.Hour)
		if slotEnd.After(window.End) {
			slotEnd = window.End
		}
		hits, err := s.index.ListObservationsInWindow(ctx, scope, memoryindex.TimeWindow{Start: slot, End: slotEnd}, 0)
		if err != nil {
			return err
		}
		if len(hits) == 0 {
			continue
		}
		summary := buildRollupSummary(hits, s.cfg.Rollups.HourlySampleSize)
		if err := s.index.UpsertChatRollup(ctx, memoryindex.RollupRecord{
			Channel:           scope.Channel,
			ChatID:            scope.ChatID,
			WindowStart:       slot,
			WindowEnd:         slotEnd,
			SourceCount:       len(hits),
			LastObservationAt: hits[len(hits)-1].CreatedAt,
			Summary:           summary,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ensureEmbeddings(ctx context.Context) error {
	_, err := s.ensureEmbeddingsBatch(ctx)
	return err
}

func (s *Service) ensureEmbeddingsBatch(ctx context.Context) (int, error) {
	if s.embedder == nil {
		return 0, nil
	}
	missing, err := s.index.ListObservationsMissingEmbeddings(
		ctx,
		s.embedder.ModelName(),
		max(16, s.embedder.MaxBatch()),
		s.embedder.MinContentChars(),
	)
	if err != nil || len(missing) == 0 {
		return 0, err
	}
	inputs := make([]string, 0, len(missing))
	ids := make([]int64, 0, len(missing))
	for _, row := range missing {
		ids = append(ids, row.ID)
		inputs = append(inputs, row.Content)
	}
	if len(inputs) == 0 {
		return 0, nil
	}
	vectors, err := s.embedder.EmbedDocuments(ctx, inputs)
	if err != nil {
		return 0, err
	}
	for idx := range vectors {
		if err := s.index.UpsertObservationEmbedding(ctx, ids[idx], s.embedder.ModelName(), vectors[idx]); err != nil {
			return 0, err
		}
	}
	return len(vectors), nil
}

func (s *Service) startStartupBackfill() {
	if s == nil || s.embedder == nil || !s.cfg.Embeddings.Enabled {
		return
	}
	go s.runStartupBackfill(context.Background())
}

func (s *Service) runStartupBackfill(ctx context.Context) {
	for {
		count, err := s.ensureEmbeddingsBatch(ctx)
		if err != nil {
			log.Printf("chatmemory: startup embedding backfill failed: %v", err)
			timer := time.NewTimer(s.backfillDelay())
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				continue
			}
		}
		if count == 0 {
			return
		}
	}
}

func (s *Service) backfillDelay() time.Duration {
	if s == nil || s.backfillRetryDelay <= 0 {
		return 30 * time.Second
	}
	return s.backfillRetryDelay
}

func buildRollupSummary(hits []memoryindex.Hit, sampleSize int) string {
	if len(hits) == 0 {
		return ""
	}
	if sampleSize <= 0 {
		sampleSize = 8
	}
	sampled := sampleHits(hits, sampleSize)
	participants := make([]string, 0)
	seenParticipants := map[string]struct{}{}

	var sb strings.Builder
	for idx, hit := range sampled {
		label := hitSpeakerLabel(hit)
		if label != "" {
			if _, ok := seenParticipants[label]; !ok {
				seenParticipants[label] = struct{}{}
				participants = append(participants, label)
			}
		}
		if idx > 0 {
			sb.WriteString(" | ")
		}
		fmt.Fprintf(&sb, "[%s] %s", formatTime(hit.CreatedAt), compactContent(hit.Content))
	}

	if len(participants) > 0 {
		return fmt.Sprintf("participants=%s; excerpts=%s", strings.Join(participants, ", "), sb.String())
	}
	return "excerpts=" + sb.String()
}

func sampleHits(hits []memoryindex.Hit, sampleSize int) []memoryindex.Hit {
	if len(hits) <= sampleSize {
		return hits
	}
	if sampleSize <= 2 {
		return []memoryindex.Hit{hits[0], hits[len(hits)-1]}
	}
	out := make([]memoryindex.Hit, 0, sampleSize)
	step := float64(len(hits)-1) / float64(sampleSize-1)
	seen := map[int]struct{}{}
	for idx := 0; idx < sampleSize; idx++ {
		sourceIdx := int(math.Round(float64(idx) * step))
		if _, ok := seen[sourceIdx]; ok {
			continue
		}
		seen[sourceIdx] = struct{}{}
		out = append(out, hits[sourceIdx])
	}
	return out
}

func hitSpeakerLabel(hit memoryindex.Hit) string {
	if label := extractHeaderValue(hit.Content, "sender_label"); label != "" {
		return label
	}
	if hit.SenderID != "" {
		return hit.SenderID
	}
	return hit.Role
}

func compactContent(content string) string {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "[telegram_group_message]") {
		if parts := strings.SplitN(content, "\n\n", 2); len(parts) == 2 {
			content = strings.TrimSpace(parts[1])
		}
	}
	return truncateRunes(content, 180)
}

func extractHeaderValue(content, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
		if line == "" {
			break
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeLabel(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func floorHour(ts time.Time) time.Time {
	return ts.UTC().Truncate(time.Hour)
}

func formatTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

func hitKey(hit memoryindex.Hit) string {
	return hit.Channel + "\x00" + hit.ChatID + "\x00" + hit.CreatedAt.UTC().Format(time.RFC3339Nano) + "\x00" + hit.Content
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
