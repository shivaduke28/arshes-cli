package websocket

import (
	"encoding/json"
	"testing"
)

func TestParseRequestID(t *testing.T) {
	tests := []struct {
		raw    string
		want   int64
		wantOK bool
	}{
		{"5", 5, true},
		{"5.0", 5, true},
		{"5e0", 5, true},
		{"5.5", 0, false},
		{`"5"`, 0, false},
		{"null", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseRequestID(json.RawMessage(tt.raw))
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("parseRequestID(%s) = %d, %v; want %d, %v", tt.raw, got, ok, tt.want, tt.wantOK)
		}
	}
}
