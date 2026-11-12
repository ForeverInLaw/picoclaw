package utils

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestDownloadFileRetriesBodyCutMidTransfer(t *testing.T) {
	retryDelayUnit = time.Millisecond
	t.Cleanup(func() { retryDelayUnit = time.Second })
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			// Promise more bytes than are sent, then drop the connection.
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("partial"))
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		_, _ = w.Write([]byte("photo"))
	}))
	defer server.Close()

	path := DownloadFile(server.URL, "photo.jpg", DownloadOptions{})
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil || string(data) != "photo" || attempts != 2 {
		t.Fatalf("got %q, %v after %d attempts; want photo after 2", data, err, attempts)
	}
}
