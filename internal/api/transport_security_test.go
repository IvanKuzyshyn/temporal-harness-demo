package api_test

import (
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestDuplicateFetchHeadersRejected(t *testing.T) {
	for _, key := range []string{"Sec-Fetch-Site", "Sec-Fetch-Mode"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:18081/models", nil)
		r.Header.Set("X-Webchat-Secret", secret)
		value := "same-origin"
		r.Header.Add(key, value)
		r.Header.Add(key, "cross-site")
		w := httptest.NewRecorder()
		handler(t).ServeHTTP(w, r)
		require.Equal(t, 403, w.Code)
	}
}
func TestMalformedPaginationEncodingRejected(t *testing.T) {
	for _, query := range []string{"cursor=%zz", "limit=%", "before=%xx", "limit=1;cursor=x"} {
		w := request(handler(t), "GET", "/conversations?"+query, "", nil)
		require.Equal(t, 400, w.Code, query)
	}
}
