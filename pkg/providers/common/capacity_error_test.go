package common

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sipeed/picoclaw/pkg/providers/protocoltypes"
)

func TestHTTPErrorRetainsTopLevelAndNestedCapacityCodes(t *testing.T) {
	for _, body := range []string{
		`{"code":"capacity_unavailable","message":"No capacity available, retry later"}`,
		`{"error":{"code":"capacity_unavailable","message":"No capacity available, retry later"}}`,
	} {
		resp := &http.Response{
			StatusCode: 503,
			Header:     http.Header{"Retry-After": {"7"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}
		err := HandleErrorResponse(resp, "https://api.example.com")
		var failure *protocoltypes.HTTPError
		if !errors.As(err, &failure) || failure.Code != "capacity_unavailable" || failure.StatusCode != 503 ||
			failure.RetryAfter != 7*time.Second {
			t.Fatalf("lost structured capacity metadata: %#v", failure)
		}
	}
}
