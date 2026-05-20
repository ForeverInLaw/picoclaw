package facts

import "errors"

var (
	ErrNotFound         = errors.New("facts: not found")
	ErrMissingEmbedding = errors.New("facts: embedding not set")
	ErrInvalidConfig    = errors.New("facts: invalid config")
)
