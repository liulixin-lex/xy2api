//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactAuditBodyStripsURLCredentialsInEveryString(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"free_text", `{"reason":"maintenance https://proxy-user:proxy-secret@relay.example/private and socks5://another-user:another-secret@other.example:1080/path done"}`, `{"reason":"maintenance https://***@relay.example/private and socks5://***@other.example:1080/path done"}`},
		{"nested_values", `{"items":["https://encoded%40user:encoded%3Apassword@relay.example/path",{"name":"http://only-user@other.example/path","count":3,"active":true,"optional":null}],"note":"ordinary text"}`, `{"items":["https://***@relay.example/path",{"name":"http://***@other.example/path","count":3,"active":true,"optional":null}],"note":"ordinary text"}`},
		{"root_string", `"check https://proxy-user:proxy-secret@relay.example/private"`, `"check https://***@relay.example/private"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := RedactAuditBody([]byte(tc.body), "application/json")
			var got, want any
			require.NoError(t, json.Unmarshal([]byte(out), &got))
			require.NoError(t, json.Unmarshal([]byte(tc.want), &want))
			require.Equal(t, want, got)
		})
	}
}
