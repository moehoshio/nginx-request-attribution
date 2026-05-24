package watcher

import (
	"testing"
)

func TestExtractLogLine(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain nginx log line",
			input:    `192.168.1.1 - - [10/Oct/2023:13:55:36 +0000] "GET /api/users HTTP/1.1" 200 1234 "-" "curl/7.68.0"`,
			expected: `192.168.1.1 - - [10/Oct/2023:13:55:36 +0000] "GET /api/users HTTP/1.1" 200 1234 "-" "curl/7.68.0"`,
		},
		{
			name:     "RFC 3164 syslog with priority",
			input:    `<190>Oct 10 13:55:36 webserver nginx: 192.168.1.1 - - [10/Oct/2023:13:55:36 +0000] "GET /api/users HTTP/1.1" 200 1234 "-" "curl/7.68.0"`,
			expected: `192.168.1.1 - - [10/Oct/2023:13:55:36 +0000] "GET /api/users HTTP/1.1" 200 1234 "-" "curl/7.68.0"`,
		},
		{
			name:     "syslog with tag and pid",
			input:    `<190>Oct 10 13:55:36 webserver nginx[1234]: 10.0.0.1 - - [10/Oct/2023:14:00:00 +0000] "POST /login HTTP/2.0" 302 0 "-" "Mozilla/5.0"`,
			expected: `10.0.0.1 - - [10/Oct/2023:14:00:00 +0000] "POST /login HTTP/2.0" 302 0 "-" "Mozilla/5.0"`,
		},
		{
			name:     "empty message",
			input:    "",
			expected: "",
		},
		{
			name:     "whitespace only",
			input:    "   ",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLogLine(tt.input)
			if got != tt.expected {
				t.Errorf("extractLogLine(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
