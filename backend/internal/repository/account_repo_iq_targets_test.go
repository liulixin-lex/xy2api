package repository

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeModelRateLimits(t *testing.T) {
	tests := []struct {
		name    string
		input   map[string]any
		want    map[string]any
		wantErr bool
	}{
		{name: "omitted remains omitted", input: map[string]any{"other": true}, want: map[string]any{"other": true}},
		{name: "nil becomes empty object", input: map[string]any{"model_rate_limits": nil}, want: map[string]any{"model_rate_limits": map[string]any{}}},
		{name: "object is retained", input: map[string]any{"model_rate_limits": map[string]any{"gpt-6": true}}, want: map[string]any{"model_rate_limits": map[string]any{"gpt-6": true}}},
		{name: "non-object is rejected", input: map[string]any{"model_rate_limits": []any{}}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeModelRateLimits(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.True(t, reflect.DeepEqual(tt.want, tt.input))
		})
	}
}
