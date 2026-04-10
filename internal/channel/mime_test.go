package channel

import (
	"strings"
	"testing"
)

// TestExtractEmailText_PlainUTF8 verifies text extraction from a plain UTF-8 email.
func TestExtractEmailText_PlainUTF8(t *testing.T) {
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: 7bit\r\n" +
		"\r\n" +
		"Сделаю до пятницы."

	got := extractEmailText(strings.NewReader(raw))
	if got != "Сделаю до пятницы." {
		t.Errorf("want %q, got %q", "Сделаю до пятницы.", got)
	}
}

// TestExtractEmailText_QuotedPrintable verifies quoted-printable decoding.
func TestExtractEmailText_QuotedPrintable(t *testing.T) {
	// Russian word for "Hello" in UTF-8 quoted-printable.
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"=D0=9F=D1=80=D0=B8=D0=B2=D0=B5=D1=8200"

	got := extractEmailText(strings.NewReader(raw))
	if !strings.HasPrefix(got, "Привет") {
		t.Errorf("want prefix %q, got %q", "Привет", got)
	}
}

// TestExtractEmailText_Base64 verifies base64 decoding.
func TestExtractEmailText_Base64(t *testing.T) {
	// Russian phrase for "Report is ready" in UTF-8, base64-encoded.
	// base64 of the above = "0J7RgtGH0ZHRgiDQs9C+0YLQvtCy"
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"0J7RgtGH0ZHRgiDQs9C+0YLQvtCy"

	got := extractEmailText(strings.NewReader(raw))
	if got != "Отчёт готов" {
		t.Errorf("want %q, got %q", "Отчёт готов", got)
	}
}

// TestExtractEmailText_MultipartAlternative_PrefersPlain verifies that in
// multipart/alternative text/plain is preferred over text/html.
func TestExtractEmailText_MultipartAlternative_PrefersPlain(t *testing.T) {
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"b1\"\r\n" +
		"\r\n" +
		"--b1\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Обычный текст\r\n" +
		"--b1\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<html><body>HTML версия</body></html>\r\n" +
		"--b1--\r\n"

	got := extractEmailText(strings.NewReader(raw))
	if got != "Обычный текст" {
		t.Errorf("want %q, got %q", "Обычный текст", got)
	}
}

// TestExtractEmailText_MultipartAlternative_FallbackToHTML verifies that when
// text/plain is absent, stripped text/html is used.
func TestExtractEmailText_MultipartAlternative_FallbackToHTML(t *testing.T) {
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"b2\"\r\n" +
		"\r\n" +
		"--b2\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<p>Только HTML</p>\r\n" +
		"--b2--\r\n"

	got := extractEmailText(strings.NewReader(raw))
	if !strings.Contains(got, "Только HTML") {
		t.Errorf("want to contain %q, got %q", "Только HTML", got)
	}
}

// TestExtractEmailText_MultipartMixed_SkipsAttachment verifies that attachments
// in multipart/mixed are skipped.
func TestExtractEmailText_MultipartMixed_SkipsAttachment(t *testing.T) {
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b3\"\r\n" +
		"\r\n" +
		"--b3\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Основной текст письма\r\n" +
		"--b3\r\n" +
		"Content-Type: application/pdf\r\n" +
		"Content-Disposition: attachment; filename=\"report.pdf\"\r\n" +
		"\r\n" +
		"(binary data)\r\n" +
		"--b3--\r\n"

	got := extractEmailText(strings.NewReader(raw))
	if got != "Основной текст письма" {
		t.Errorf("want %q, got %q", "Основной текст письма", got)
	}
}

// TestExtractEmailText_MultipartMixed_CombinesTextParts verifies that multiple
// text parts in multipart/mixed are concatenated.
func TestExtractEmailText_MultipartMixed_CombinesTextParts(t *testing.T) {
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b4\"\r\n" +
		"\r\n" +
		"--b4\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Первая часть\r\n" +
		"--b4\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Вторая часть\r\n" +
		"--b4--\r\n"

	got := extractEmailText(strings.NewReader(raw))
	if !strings.Contains(got, "Первая часть") || !strings.Contains(got, "Вторая часть") {
		t.Errorf("want both parts, got %q", got)
	}
}

// TestExtractEmailText_NonUTF8Charset verifies re-encoding from windows-1251 to UTF-8.
func TestExtractEmailText_NonUTF8Charset(t *testing.T) {
	// Russian "Hello" in windows-1251 encoding (hex: CF F0 E8 E2 E5 F2).
	body := "\xCF\xF0\xE8\xE2\xE5\xF2"
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=windows-1251\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n" +
		"\r\n" +
		body

	got := extractEmailText(strings.NewReader(raw))
	if got != "Привет" {
		t.Errorf("want %q, got %q", "Привет", got)
	}
}

// TestExtractEmailText_SkipsImages verifies that parts with content type image/* are skipped.
func TestExtractEmailText_SkipsImages(t *testing.T) {
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b5\"\r\n" +
		"\r\n" +
		"--b5\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Текст без картинки\r\n" +
		"--b5\r\n" +
		"Content-Type: image/png\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"iVBORw0KGgo=\r\n" +
		"--b5--\r\n"

	got := extractEmailText(strings.NewReader(raw))
	if got != "Текст без картинки" {
		t.Errorf("want %q, got %q", "Текст без картинки", got)
	}
}

// TestExtractEmailText_NoContentType verifies behaviour when the Content-Type header is absent.
func TestExtractEmailText_NoContentType(t *testing.T) {
	raw := "From: alice@example.com\r\n" +
		"\r\n" +
		"Простой текст без заголовков MIME"

	got := extractEmailText(strings.NewReader(raw))
	if got != "Простой текст без заголовков MIME" {
		t.Errorf("want %q, got %q", "Простой текст без заголовков MIME", got)
	}
}

// TestExtractEmailText_StripHTML verifies HTML tag removal.
func TestExtractEmailText_StripHTML(t *testing.T) {
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<html><head><style>body{color:red}</style></head>" +
		"<body><p>Важное сообщение</p><br>Вторая строка</body></html>"

	got := extractEmailText(strings.NewReader(raw))
	if !strings.Contains(got, "Важное сообщение") {
		t.Errorf("want to contain %q, got %q", "Важное сообщение", got)
	}
	if !strings.Contains(got, "Вторая строка") {
		t.Errorf("want to contain %q, got %q", "Вторая строка", got)
	}
	if strings.Contains(got, "<") || strings.Contains(got, ">") {
		t.Errorf("result must not contain HTML tags, got %q", got)
	}
	if strings.Contains(got, "color:red") {
		t.Errorf("CSS must not be present in result, got %q", got)
	}
}

// TestNormalizeLines verifies blank-line normalisation.
func TestNormalizeLines(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "без лишних пустых строк",
			input: "строка1\nстрока2",
			want:  "строка1\nстрока2",
		},
		{
			name:  "три пустых строки схлопываются в одну пустую",
			input: "строка1\n\n\n\nстрока2",
			want:  "строка1\n\nстрока2",
		},
		{
			name:  "только whitespace возвращает пустую строку",
			input: "   \n  \t  ",
			want:  "",
		},
		{
			name:  "пустой ввод",
			input: "",
			want:  "",
		},
		{
			name:  "ровно две пустые строки не меняются",
			input: "строка1\n\nстрока2",
			want:  "строка1\n\nстрока2",
		},
		{
			name:  "CRLF нормализуется в LF",
			input: "строка1\r\nстрока2",
			want:  "строка1\nстрока2",
		},
		{
			name:  "три CRLF схлопываются как обычные пустые строки",
			input: "строка1\r\n\r\n\r\nстрока2",
			want:  "строка1\n\nстрока2",
		},
		{
			name:  "одиночный CR нормализуется в LF",
			input: "строка1\rстрока2",
			want:  "строка1\nстрока2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeLines(tc.input)
			if got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

// TestSplitEmailReply verifies splitting an email into reply and quote.
func TestSplitEmailReply(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantReply string
		wantQuote string
	}{
		{
			name:      "без цитаты",
			input:     "да, сделаю",
			wantReply: "да, сделаю",
			wantQuote: "",
		},
		{
			name:      "цитата с символом >",
			input:     "да, сделаю\n\n> оригинальное письмо\n> продолжение",
			wantReply: "да, сделаю",
			wantQuote: "оригинальное письмо\nпродолжение",
		},
		{
			name:      "On ... wrote: маркер",
			input:     "да, сделаю\n\nOn Thu, Apr 10, 2026 at 10:00 AM, Someone wrote:\n> оригинал",
			wantReply: "да, сделаю",
			wantQuote: "On Thu, Apr 10, 2026 at 10:00 AM, Someone wrote:\nоригинал",
		},
		{
			name:      "Outlook маркер",
			input:     "да, сделаю\n\n-----Original Message-----\nFrom: someone@example.com",
			wantReply: "да, сделаю",
			wantQuote: "-----Original Message-----\nFrom: someone@example.com",
		},
		{
			name:      "вложенные >> схлопываются на один уровень",
			input:     "ответ\n\n>> вложенная цитата",
			wantReply: "ответ",
			wantQuote: "> вложенная цитата",
		},
		{
			name:      "пустое тело",
			input:     "",
			wantReply: "",
			wantQuote: "",
		},
		{
			name:      "письмо без ответа только цитата",
			input:     "> только цитата\n> продолжение",
			wantReply: "",
			wantQuote: "только цитата\nпродолжение",
		},
		{
			name:      "Gmail двухстрочный On ... wrote:",
			input:     "да, сделаю\n\nOn Thu, Apr 10, 2026 at 10:00 AM, John Smith\n<john@example.com> wrote:\nоригинальное письмо",
			wantReply: "да, сделаю",
			wantQuote: "On Thu, Apr 10, 2026 at 10:00 AM, John Smith\n<john@example.com> wrote:\nоригинальное письмо",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotReply, gotQuote := splitEmailReply(tc.input)
			if gotReply != tc.wantReply {
				t.Errorf("reply: want %q, got %q", tc.wantReply, gotReply)
			}
			if gotQuote != tc.wantQuote {
				t.Errorf("quote: want %q, got %q", tc.wantQuote, gotQuote)
			}
		})
	}
}

// TestStripHTML_RemovesTagsAndStyle verifies the stripHTML function directly.
func TestStripHTML_RemovesTagsAndStyle(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "простые теги",
			input: "<p>Текст</p>",
			want:  "Текст",
		},
		{
			name:  "script удаляется вместе с содержимым",
			input: "До<script>alert(1)</script>После",
			want:  "ДоПосле",
		},
		{
			name:  "style удаляется вместе с содержимым",
			input: "До<style>.x{color:red}</style>После",
			want:  "ДоПосле",
		},
		{
			name:  "br превращается в перенос строки",
			input: "Строка1<br>Строка2",
			want:  "Строка1\nСтрока2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.TrimSpace(stripHTML(tc.input))
			if got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}
