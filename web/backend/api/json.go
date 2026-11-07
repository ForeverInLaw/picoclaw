package api

import (
	"encoding/json"
	"io"
	"net/http"
)

// decodeJSONObject decodes a request body of exactly one JSON value, at most
// 1 MiB in all, into into, refusing unknown fields. A larger body fails the
// read, so nothing past the limit is ever ignored.
func decodeJSONObject(w http.ResponseWriter, r *http.Request, into any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(into) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

// writeJSON answers with status and body as JSON.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeAPIError answers with status and the dashboard's error shape,
// {"status":"error","message":message}, whose message the UI shows as is.
func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"status": "error", "message": message})
}
