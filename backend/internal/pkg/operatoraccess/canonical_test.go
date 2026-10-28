//go:build unit

package operatoraccess

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckCanonicalRejectsDuplicateAndFoldedKeys(t *testing.T) {
	for _, body := range []string{`{"balance":1,"balance":2}`, `{"Balance":1,"balance":2}`, `{"k":1,"K":2}`, `{"x":[{"K":1,"K":2}]}`, `{"x":{"a":1,"A":2}}`} {
		require.Error(t, CheckCanonical("application/json", []byte(body), nil, nil), body)
	}
	require.NoError(t, CheckCanonical("application/json; charset=utf-8", []byte(`{"balance":1,"nested":{"balance":2}}`), nil, nil))
}
func TestCheckCanonicalRejectsTrailingDataAndNonObjects(t *testing.T) {
	for _, body := range []string{`{} {}`, `{}garbage`, `[]`, `null`, `1`, `{"x":}`, "{\"x\":\"\xff\"}", `{"x":` + strings.Repeat("[", 64) + "0" + strings.Repeat("]", 64) + "}"} {
		require.Error(t, CheckCanonical("application/json", []byte(body), nil, nil), body)
	}
	require.NoError(t, CheckCanonical("application/json", []byte(`{"x":`+strings.Repeat("[", 63)+"0"+strings.Repeat("]", 63)+"}"), nil, nil))
}
func TestCheckCanonicalRejectsNonJSONContentType(t *testing.T) {
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "application/json; broken"} {
		require.Error(t, CheckCanonical(ct, []byte("{}"), nil, nil))
	}
	require.NoError(t, CheckCanonical("", nil, nil, nil))
}
func TestCheckCanonicalRejectsRepeatedQueryKeys(t *testing.T) {
	err := CheckCanonical("", nil, url.Values{"search": {"a", "b"}}, []string{"search"})
	require.Equal(t, &CanonicalError{Field: "search"}, err)
	require.NoError(t, CheckCanonical("", nil, url.Values{"other": {"a", "b"}}, []string{"search"}))
}
func TestFieldLookupMatchesEncodingJSON(t *testing.T) {
	for _, key := range []string{"balance", "Balance", "BALANCE", "k", "K", "K", "s", "ſ"} {
		raw, _ := json.Marshal(map[string]int{key: 13})
		var obj map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(raw, &obj))
		var bound struct {
			Balance int `json:"balance"`
			K       int `json:"k"`
			S       int `json:"s"`
		}
		require.NoError(t, json.Unmarshal(raw, &bound))
		for name, want := range map[string]int{"balance": bound.Balance, "k": bound.K, "s": bound.S} {
			v, ok := Field(obj, name)
			if want == 0 {
				require.False(t, ok)
			} else {
				require.True(t, ok)
				var got int
				require.NoError(t, json.Unmarshal(v, &got))
				require.Equal(t, want, got)
			}
		}
	}
}
