package telegram

import "testing"

func TestAccountDisplayName(t *testing.T) {
	tests := []struct {
		name    string
		account Account
		want    string
	}{
		{"full name", Account{ID: 1, FirstName: "Alice", LastName: "Smith", Username: "alice"}, "Alice Smith"},
		{"username", Account{ID: 2, Username: "bob"}, "@bob"},
		{"id", Account{ID: 3}, "3"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.account.DisplayName(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
