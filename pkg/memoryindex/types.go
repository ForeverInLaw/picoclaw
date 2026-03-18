package memoryindex

import "time"

type Config struct {
	MaxResults      int
	MaxSnippetChars int
	MinQueryChars   int
}

type Observation struct {
	SessionKey string
	Channel    string
	ChatID     string
	PeerKind   string
	ChatLabel  string
	Role       string
	SenderID   string
	SourceKind string
	SourceKey  string
	Content    string
	CreatedAt  time.Time
}

type ChatScope struct {
	Channel  string
	ChatID   string
	PeerKind string
	Label    string
}

type SearchRequest struct {
	Query      string
	SessionKey string
	Channel    string
	ChatID     string
	ChatScopes []ChatScope
	Since      time.Time
	Until      time.Time
	Limit      int
}

type Hit struct {
	SessionKey string
	Channel    string
	ChatID     string
	PeerKind   string
	ChatLabel  string
	Role       string
	SenderID   string
	Content    string
	CreatedAt  time.Time
}

type ChatCatalogEntry struct {
	Channel  string
	ChatID   string
	PeerKind string
	Label    string
	LastSeen time.Time
}

type ChatAccessEntry struct {
	ChatCatalogEntry
	Alias string
}

type ChatAliasRecord struct {
	Alias             string
	Channel           string
	ChatID            string
	Label             string
	AllowedRequesters []string
}

type ChatParticipantRecord struct {
	Channel    string
	ChatID     string
	SenderID   string
	Label      string
	LastSeenAt time.Time
}

type TimeWindow struct {
	Start time.Time
	End   time.Time
}

type RollupRecord struct {
	Channel           string
	ChatID            string
	WindowStart       time.Time
	WindowEnd         time.Time
	SourceCount       int
	LastObservationAt time.Time
	Summary           string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
