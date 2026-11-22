package httputil

import "testing"

func TestHasUniqueJSONMembersRecursive(t *testing.T) {
	tests := []struct {
		name, body string
		want       bool
	}{
		{"nested", `{"a":{"b":[{"c":1}]}}`, true},
		{"escaped duplicate", `{"a":1,"\u0061":2}`, false},
		{"trailing", `{"a":1} {"b":2}`, false},
		{"primitive", `null`, true},
		{"nested duplicate", `{"a":[{"x":1,"x":2}]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasUniqueJSONMembers([]byte(tt.body)); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}
