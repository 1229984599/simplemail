package cf

import "testing"

func TestExtractBaseDomain(t *testing.T) {
	tests := []struct {
		input string
		want  string
		err   bool
	}{
		{"sub.example.com", "example.com", false},
		{"vet.nightunderfly.online", "nightunderfly.online", false},
		{"a.b.c.d", "c.d", false},
		{"example.com", "", true},
		{"com", "", true},
		{"", "", true},
		{"single", "", true},
	}
	for _, tt := range tests {
		got, err := ExtractBaseDomain(tt.input)
		if (err != nil) != tt.err {
			t.Errorf("ExtractBaseDomain(%q) error = %v, want err %v", tt.input, err, tt.err)
			continue
		}
		if got != tt.want {
			t.Errorf("ExtractBaseDomain(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
