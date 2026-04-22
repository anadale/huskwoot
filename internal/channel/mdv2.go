package channel

import (
	"regexp"
	"strings"
)

var (
	reHeader = regexp.MustCompile(`^#{1,6}\s+(.+)$`)
	reBold   = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
)

// tgV2Special contains all characters that must be backslash-escaped in Telegram MarkdownV2.
const tgV2Special = `\_*[]()~` + "`" + `>#+-=|{}.!\`

// escapeTgV2 backslash-escapes all MarkdownV2 special characters in plain text.
func escapeTgV2(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if strings.ContainsRune(tgV2Special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// escapeCodeContent escapes the characters Telegram requires inside code entities: ` and \.
func escapeCodeContent(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}

// mdToTelegramV2 converts a subset of standard Markdown to Telegram MarkdownV2.
// Handles: fenced code blocks (```), inline code (`…`), ## headers, bold (**…**/__…__).
// All MarkdownV2 special characters in plain text are escaped.
func mdToTelegramV2(s string) string {
	var out strings.Builder
	lines := strings.Split(s, "\n")
	inFence := false

	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if !inFence {
				out.WriteString(line)
				inFence = true
			} else {
				out.WriteString("```")
				inFence = false
			}
			continue
		}
		if inFence {
			out.WriteString(escapeCodeContent(line))
			continue
		}
		out.WriteString(convertMdLine(line))
	}
	return out.String()
}

func convertMdLine(line string) string {
	if m := reHeader.FindStringSubmatch(line); m != nil {
		return "*" + escapeTgV2(m[1]) + "*"
	}
	return splitOnInlineCode(line)
}

// splitOnInlineCode splits s on inline code spans, converting non-code segments
// and passing code spans through (with required content escaping).
func splitOnInlineCode(s string) string {
	var out strings.Builder
	for {
		start := strings.IndexByte(s, '`')
		if start < 0 {
			out.WriteString(convertBoldAndEscape(s))
			break
		}
		out.WriteString(convertBoldAndEscape(s[:start]))
		end := strings.IndexByte(s[start+1:], '`')
		if end < 0 {
			// Unclosed backtick: treat as plain text.
			out.WriteString(escapeTgV2(s[start:]))
			break
		}
		end = start + 1 + end
		out.WriteByte('`')
		out.WriteString(escapeCodeContent(s[start+1 : end]))
		out.WriteByte('`')
		s = s[end+1:]
	}
	return out.String()
}

// convertBoldAndEscape converts **bold**/__bold__ to MarkdownV2 bold and escapes plain text.
func convertBoldAndEscape(s string) string {
	var out strings.Builder
	for len(s) > 0 {
		loc := reBold.FindStringIndex(s)
		if loc == nil {
			out.WriteString(escapeTgV2(s))
			break
		}
		out.WriteString(escapeTgV2(s[:loc[0]]))
		m := reBold.FindStringSubmatch(s[loc[0]:loc[1]])
		content := m[1]
		if content == "" {
			content = m[2]
		}
		out.WriteString("*")
		out.WriteString(escapeTgV2(content))
		out.WriteString("*")
		s = s[loc[1]:]
	}
	return out.String()
}
