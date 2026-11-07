package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONObjectAcceptsOneValueWithinTheSizeLimit(t *testing.T) {
	padding := strings.Repeat(" ", 1<<20)
	cases := map[string]struct {
		body string
		want bool
	}{
		"one object":                        {`{"name":"orgs"}`, true},
		"trailing value":                    {`{"name":"orgs"} {"name":"more"}`, false},
		"unknown field":                     {`{"name":"orgs","extra":1}`, false},
		"trailing data past the limit":      {`{"name":"orgs"}` + padding + `{"name":"more"}`, false},
		"whitespace running past the limit": {`{"name":"orgs"}` + padding, false},
		"value larger than the limit":       {`{"name":"` + strings.Repeat("x", 1<<20) + `"}`, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var into struct {
				Name string `json:"name"`
			}
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			if got := decodeJSONObject(httptest.NewRecorder(), request, &into); got != tc.want {
				t.Fatalf("decodeJSONObject() = %t, want %t", got, tc.want)
			}
		})
	}
}
