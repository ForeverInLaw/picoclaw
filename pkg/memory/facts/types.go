package facts

import "time"

// Fact is an atomic memory tuple.
type Fact struct {
	ID            int64
	Namespace     string
	Entity        string
	Attribute     string
	Value         string
	Confidence    float64
	SourceMsgRef  string
	SourceActor   string    // empty when bot-inferred
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastSeenAt    time.Time
	AccessCount   int64
	TTLSeconds    int64     // 0 = use config default
	DeletedAt     time.Time // zero when not deleted
	Embedding     []float32 // nil if not yet embedded
	EmbeddingNorm float64
}

// Namespace formatting helpers.
const (
	NSPrefixTGChat = "tg:chat:"
	NSPrefixTGUser = "tg:user:"
	NSPrefixTGBot  = "tg:bot:"
)

// RecallHit is a kNN search result.
type RecallHit struct {
	Fact  Fact
	Score float64
}
