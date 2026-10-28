//go:build unit

package admin

import (
	"github.com/liulixin-lex/xy2api/internal/service"
	"github.com/stretchr/testify/require"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestOperatorGroupFieldListCoversUpdateRequest(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[CreateGroupRequest](), reflect.TypeFor[UpdateGroupRequest]()} {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			if tag == "" || tag == "-" {
				continue
			}
			name := strings.ReplaceAll(strings.ReplaceAll(f.Name, "Tts", "TTS"), "Stt", "STT")
			require.True(t, slices.Contains(service.OperatorGroupPriceFields, name) || slices.Contains(service.OperatorGroupFreeFields, name), "%s %s", f.Name, tag)
		}
	}
}
