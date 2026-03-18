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
	Role       string
	SenderID   string
	SourceKind string
	SourceKey  string
	Content    string
	CreatedAt  time.Time
}

type SearchRequest struct {
	Query      string
	SessionKey string
	Channel    string
	ChatID     string
	Limit      int
}

type Hit struct {
	SessionKey string
	Channel    string
	ChatID     string
	Role       string
	SenderID   string
	Content    string
	CreatedAt  time.Time
}
