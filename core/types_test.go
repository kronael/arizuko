package core

import "testing"

func TestChat_IsSingleUser(t *testing.T) {
	tests := []struct {
		name    string
		isGroup bool
		want    bool
	}{
		{"dm or slink", false, true},
		{"group chat", true, false},
	}
	for _, tt := range tests {
		got := Chat{IsGroup: tt.isGroup}.IsSingleUser()
		if got != tt.want {
			t.Errorf("%s: IsSingleUser() = %v, want %v", tt.name, got, tt.want)
		}
	}
}
