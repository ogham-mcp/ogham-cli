package native

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// testServer is a thin alias to flag intent at call sites.
type testServer = httptest.Server

// startFakeServer returns an httptest.Server that always responds with
// the given JSON body and HTTP 200. Cleanup is registered on t so
// callers don't need a defer.
func startFakeServer(t *testing.T, body string) *testServer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// floatSliceJSON renders []float32 as a JSON array literal -- used by
// fake API servers to hand-roll response bodies without a full
// json.Marshal round-trip.
func floatSliceJSON(vec []float32) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, v := range vec {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.FormatFloat(float64(v), 'g', -1, 32))
	}
	sb.WriteByte(']')
	return sb.String()
}
