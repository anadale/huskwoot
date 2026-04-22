package channel

import "testing"

func TestMdToTelegramV2(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain text without special chars",
			input: "Hello world",
			want:  "Hello world",
		},
		{
			name:  "dots escaped",
			input: "до 25.05.2025",
			want:  `до 25\.05\.2025`,
		},
		{
			name:  "parens escaped",
			input: "task (open)",
			want:  `task \(open\)`,
		},
		{
			name:  "dash escaped",
			input: "item - value",
			want:  `item \- value`,
		},
		{
			name:  "header level 2",
			input: "## Inbox",
			want:  "*Inbox*",
		},
		{
			name:  "header with special chars",
			input: "## My Project #1",
			want:  `*My Project \#1*`,
		},
		{
			name:  "header level 1",
			input: "# Title",
			want:  "*Title*",
		},
		{
			name:  "inline code preserved",
			input: "`inbox#1`: summary",
			want:  "`inbox#1`: summary",
		},
		{
			name:  "inline code, parens in plain text escaped",
			input: "`inbox#1`: summary (open)",
			want:  "`inbox#1`: summary \\(open\\)",
		},
		{
			name:  "inline code, date escaped",
			input: "`inbox#2`: task (open, до 01.02.2025)",
			want:  "`inbox#2`: task \\(open, до 01\\.02\\.2025\\)",
		},
		{
			name:  "bold double asterisk",
			input: "**важно** текст",
			want:  "*важно* текст",
		},
		{
			name:  "bold double underscore",
			input: "__важно__",
			want:  "*важно*",
		},
		{
			name:  "bold with special chars inside",
			input: "**v0.1.0**",
			want:  `*v0\.1\.0*`,
		},
		{
			name:  "fenced code block passed through",
			input: "```\ncode here\n```",
			want:  "```\ncode here\n```",
		},
		{
			name:  "fenced code block with lang tag",
			input: "```go\nfunc main() {}\n```",
			want:  "```go\nfunc main() {}\n```",
		},
		{
			name:  "multiline: header and task lines",
			input: "## Inbox\n`inbox#1`: задача (open)\n`inbox#2`: другая (open, до 01.02.2025)",
			want:  "*Inbox*\n`inbox#1`: задача \\(open\\)\n`inbox#2`: другая \\(open, до 01\\.02\\.2025\\)",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mdToTelegramV2(tt.input)
			if got != tt.want {
				t.Errorf("mdToTelegramV2(%q)\n got:  %q\n want: %q", tt.input, got, tt.want)
			}
		})
	}
}
