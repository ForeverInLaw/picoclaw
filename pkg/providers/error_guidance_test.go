package providers

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

func TestRejectedAttachmentsDoNotClaimTheModelLacksVision(t *testing.T) {
	for _, message := range []string{
		"Unsupported file type. Supported formats: text or images",
		"unsupported image format",
		"This model does not support image inputs",
		"document attachment not allowed",
	} {
		guidance := ErrorGuidance(&protocoltypes.HTTPError{StatusCode: 400, Wrapped: errors.New(message)})
		require.Contains(t, guidance, "provider rejected")
		require.Contains(t, guidance, "supported input types")
		require.NotContains(t, guidance, "vision-capable model")
		require.NotContains(t, guidance, "model does not support")
	}
}
