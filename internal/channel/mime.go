package channel

import (
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"strings"

	"golang.org/x/text/encoding/htmlindex"
)

// extractEmailText extracts the text content from a raw RFC 2822 email message.
// Skips images, attachments, and non-text headers.
// The result is decoded to UTF-8.
func extractEmailText(r io.Reader) string {
	msg, err := mail.ReadMessage(r)
	if err != nil {
		return ""
	}
	ct := msg.Header.Get("Content-Type")
	cte := msg.Header.Get("Content-Transfer-Encoding")
	return extractPartText(msg.Body, ct, cte, false)
}

// extractPartText recursively extracts text from a MIME part.
// isAttachment indicates whether the part is marked as an attachment.
func extractPartText(r io.Reader, contentType, transferEncoding string, isAttachment bool) string {
	if isAttachment {
		return ""
	}

	if contentType == "" {
		data, _ := io.ReadAll(r)
		return strings.TrimSpace(string(data))
	}

	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		data, _ := io.ReadAll(r)
		return strings.TrimSpace(string(data))
	}

	switch {
	case mediaType == "text/plain":
		data, _ := io.ReadAll(decodeTransfer(r, transferEncoding))
		return normalizeLines(decodeToUTF8(data, params["charset"]))

	case mediaType == "text/html":
		data, _ := io.ReadAll(decodeTransfer(r, transferEncoding))
		text := decodeToUTF8(data, params["charset"])
		return stripHTML(text)

	case mediaType == "multipart/alternative":
		return extractAlternative(r, params["boundary"])

	case strings.HasPrefix(mediaType, "multipart/"):
		return extractMixed(r, params["boundary"])

	default:
		// Skip image/*, application/*, and other non-text types.
		return ""
	}
}

// decodeTransfer wraps the reader according to the Content-Transfer-Encoding.
func decodeTransfer(r io.Reader, encoding string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	case "base64":
		// Email base64 contains line breaks — strip them before decoding.
		all, err := io.ReadAll(r)
		if err != nil {
			return strings.NewReader("")
		}
		clean := strings.Map(func(ch rune) rune {
			if ch == '\n' || ch == '\r' || ch == ' ' || ch == '\t' {
				return -1
			}
			return ch
		}, string(all))
		// Strip padding (=) before decoding: RawStdEncoding handles both
		// standard RFC 2045 base64 (with =) and unpadded base64.
		clean = strings.TrimRight(clean, "=")
		return base64.NewDecoder(base64.RawStdEncoding, strings.NewReader(clean))
	default:
		return r
	}
}

// decodeToUTF8 converts bytes from the given charset to UTF-8.
// Returns the bytes as-is if charset is empty or already UTF-8.
func decodeToUTF8(data []byte, charset string) string {
	if charset == "" {
		return string(data)
	}
	lower := strings.ToLower(strings.TrimSpace(charset))
	if lower == "utf-8" || lower == "us-ascii" || lower == "ascii" {
		return string(data)
	}
	enc, err := htmlindex.Get(charset)
	if err != nil {
		return string(data)
	}
	decoded, err := enc.NewDecoder().Bytes(data)
	if err != nil {
		return string(data)
	}
	return string(decoded)
}

// extractAlternative extracts the best text variant from multipart/alternative.
// Prefers text/plain; falls back to stripped text/html when absent.
func extractAlternative(r io.Reader, boundary string) string {
	if boundary == "" {
		return ""
	}
	mr := multipart.NewReader(r, boundary)
	var plainText, htmlText string
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		ct := part.Header.Get("Content-Type")
		cte := part.Header.Get("Content-Transfer-Encoding")
		disp, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		text := extractPartText(part, ct, cte, disp == "attachment")

		mediaType, _, _ := mime.ParseMediaType(ct)
		switch mediaType {
		case "text/plain":
			if plainText == "" {
				plainText = text
			}
		case "text/html":
			if htmlText == "" {
				htmlText = text
			}
		}
	}
	if plainText != "" {
		return plainText
	}
	return htmlText
}

// extractMixed extracts and joins all text parts from multipart/mixed and similar.
// Attachments (Content-Disposition: attachment) and non-text parts are skipped.
func extractMixed(r io.Reader, boundary string) string {
	if boundary == "" {
		return ""
	}
	mr := multipart.NewReader(r, boundary)
	var parts []string
	for {
		part, err := mr.NextPart()
		if err != nil {
			break
		}
		ct := part.Header.Get("Content-Type")
		cte := part.Header.Get("Content-Transfer-Encoding")
		disp, _, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
		text := extractPartText(part, ct, cte, disp == "attachment")
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

var (
	reScriptStyle = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reBlockTags   = regexp.MustCompile(`(?i)<(br|p|div|tr|li|h[1-6])(\s[^>]*)?>`)
	reTags        = regexp.MustCompile(`<[^>]+>`)
	reSpaces      = regexp.MustCompile(`[ \t]+`)
	reNBSP        = regexp.MustCompile(`&nbsp;`)
	reExtraLines  = regexp.MustCompile(`\n{3,}`)
	reOnWrote     = regexp.MustCompile(`(?i)^On\s+.+\s+wrote:\s*$`)
	reOutlookMark = regexp.MustCompile(`(?i)^-{5}Original Message-{5}`)
)

// normalizeLines normalizes CRLF, collapses more than two consecutive blank lines,
// and trims leading/trailing whitespace.
func normalizeLines(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = reExtraLines.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

// stripHTML removes HTML tags from text and returns readable plain text.
// Replaces &nbsp; with spaces and normalizes blank lines.
func stripHTML(html string) string {
	text := reScriptStyle.ReplaceAllString(html, "")
	text = reBlockTags.ReplaceAllString(text, "\n")
	text = reTags.ReplaceAllString(text, "")
	text = reNBSP.ReplaceAllString(text, " ")
	text = reSpaces.ReplaceAllString(text, " ")
	return normalizeLines(text)
}

// splitEmailReply splits the email text into a reply and a quote.
// Quote markers are: lines starting with >, "On ... wrote:", "-----Original Message-----".
// Supports the two-line Gmail format where "On ... wrote:" may span two lines.
// reply is the text before the first marker (trimmed); quote is the text from the marker
// onwards with single-level "> " or ">" prefixes stripped from each line.
func splitEmailReply(text string) (reply, quote string) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")

		isQuoteMarker := strings.HasPrefix(trimmed, ">") ||
			reOnWrote.MatchString(strings.TrimSpace(line)) ||
			reOutlookMark.MatchString(trimmed)

		// Gmail format: "On ... wrote:" may be split across two lines.
		// Check the current line + next line joined together.
		// Require @ to avoid false matches with ordinary text ("On balance, Alice wrote:").
		if !isQuoteMarker && i+1 < len(lines) {
			combined := strings.TrimSpace(line) + " " + strings.TrimSpace(lines[i+1])
			if reOnWrote.MatchString(combined) && strings.Contains(combined, "@") {
				isQuoteMarker = true
			}
		}

		if isQuoteMarker {
			reply = strings.TrimSpace(strings.Join(lines[:i], "\n"))
			rawQuote := strings.Join(lines[i:], "\n")
			quote = strings.TrimSpace(stripQuotePrefix(rawQuote))
			return
		}
	}
	return strings.TrimSpace(text), ""
}

// stripQuotePrefix removes a single-level "> " or ">" prefix from each line.
func stripQuotePrefix(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "> ") {
			lines[i] = line[2:]
		} else if strings.HasPrefix(line, ">") {
			lines[i] = line[1:]
		}
	}
	return strings.Join(lines, "\n")
}
