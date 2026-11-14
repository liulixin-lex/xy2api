package service

import (
	"net/http"
	"testing"

	infraerrors "github.com/liulixin-lex/xy2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestNormalizeGroupAlias(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		valid       bool
	}{
		{"", "", true}, {"  \t", "", false}, {"  中文 ", "中文", true},
		{"AB12", "AB12", true}, {"中AB", "中AB", true}, {"ＡＢ", "ＡＢ", true},
		{"ｶﾀｶﾅ", "ｶﾀｶﾅ", true}, {"αβγδ", "αβγδ", true}, {"😀😀", "😀😀", true},
		{"\x00", "", false}, {"中文A", "", false}, {"ABCDE", "", false}, {"ＡＢＣ", "", false},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := normalizeGroupAlias(tc.input)
			if !tc.valid {
				require.Error(t, err)
				require.Equal(t, http.StatusBadRequest, infraerrors.Code(err))
				require.Equal(t, "INVALID_GROUP_ALIAS", infraerrors.Reason(err))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
