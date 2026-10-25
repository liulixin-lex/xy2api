package handler

import "testing"

func TestShouldTraceGeminiProviderAction(t *testing.T) {
	for _, action := range []struct {
		name string
		want bool
	}{
		{name: "generateContent", want: true},
		{name: "streamGenerateContent", want: true},
		{name: "countTokens", want: false},
		{name: "unknown", want: false},
	} {
		t.Run(action.name, func(t *testing.T) {
			if got := shouldTraceGeminiProviderAction(action.name); got != action.want {
				t.Fatalf("shouldTraceGeminiProviderAction(%q) = %t, want %t", action.name, got, action.want)
			}
		})
	}
}
