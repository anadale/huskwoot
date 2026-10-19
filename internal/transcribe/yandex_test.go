package transcribe

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/anadale/huskwoot/internal/config"
	"github.com/anadale/huskwoot/internal/model"
)

func TestYandex_Name(t *testing.T) {
	y := newYandex(config.YandexConfig{}, &http.Client{})
	if y.Name() != "yandex" {
		t.Errorf("Name() = %q, want %q", y.Name(), "yandex")
	}
}

func TestYandex_Transcribe(t *testing.T) {
	audioData := []byte("fake-ogg-audio-data")

	type serverResponse struct {
		status int
		body   string
	}

	tests := []struct {
		name           string
		response       serverResponse
		sleep          time.Duration
		timeout        time.Duration
		audio          model.AudioInput
		wantText       string
		wantErr        bool
		wantErrContain string
		checkReq       func(t *testing.T, r *http.Request, body []byte)
	}{
		{
			name: "success",
			response: serverResponse{
				status: 200,
				body:   `{"result":"привет мир"}`,
			},
			audio:    model.AudioInput{Data: audioData, MimeType: "audio/ogg", Duration: 3 * time.Second},
			wantText: "привет мир",
		},
		{
			name: "trims whitespace",
			response: serverResponse{
				status: 200,
				body:   `{"result":"  hello world  "}`,
			},
			audio:    model.AudioInput{Data: audioData, MimeType: "audio/ogg", Duration: 2 * time.Second},
			wantText: "hello world",
		},
		{
			name: "empty result",
			response: serverResponse{
				status: 200,
				body:   `{"result":""}`,
			},
			audio:    model.AudioInput{Data: audioData, MimeType: "audio/ogg", Duration: 1 * time.Second},
			wantText: "",
		},
		{
			name: "whitespace-only result",
			response: serverResponse{
				status: 200,
				body:   `{"result":"   "}`,
			},
			audio:    model.AudioInput{Data: audioData, MimeType: "audio/ogg", Duration: 1 * time.Second},
			wantText: "",
		},
		{
			name: "non-2xx 412",
			response: serverResponse{
				status: 412,
				body:   "Precondition Failed: unsupported format",
			},
			audio:          model.AudioInput{Data: audioData, MimeType: "audio/ogg"},
			wantErr:        true,
			wantErrContain: "status 412",
		},
		{
			name: "non-2xx 500",
			response: serverResponse{
				status: 500,
				body:   "Internal Server Error",
			},
			audio:          model.AudioInput{Data: audioData, MimeType: "audio/ogg"},
			wantErr:        true,
			wantErrContain: "status 500",
		},
		{
			name: "non-2xx includes body in error",
			response: serverResponse{
				status: 400,
				body:   "bad request: missing folderId",
			},
			audio:          model.AudioInput{Data: audioData, MimeType: "audio/ogg"},
			wantErr:        true,
			wantErrContain: "missing folderId",
		},
		{
			name:    "timeout",
			sleep:   200 * time.Millisecond,
			timeout: 50 * time.Millisecond,
			response: serverResponse{
				status: 200,
				body:   `{"result":"hello"}`,
			},
			audio:          model.AudioInput{Data: audioData, MimeType: "audio/ogg"},
			wantErr:        true,
			wantErrContain: "context deadline exceeded",
		},
		{
			name: "unsupported mime type",
			response: serverResponse{
				status: 200,
				body:   `{"result":"ok"}`,
			},
			audio:          model.AudioInput{Data: audioData, MimeType: "audio/flac"},
			wantErr:        true,
			wantErrContain: "unsupported audio format",
		},
		{
			name: "mp3 audio uses mp3 format",
			response: serverResponse{
				status: 200,
				body:   `{"result":"mp3 result"}`,
			},
			audio: model.AudioInput{Data: audioData, MimeType: "audio/mpeg"},
			checkReq: func(t *testing.T, r *http.Request, body []byte) {
				t.Helper()
				if got := r.URL.Query().Get("format"); got != "mp3" {
					t.Errorf("format query param = %q, want %q", got, "mp3")
				}
			},
			wantText: "mp3 result",
		},
		{
			name: "wav audio uses lpcm format with sampleRateHertz",
			response: serverResponse{
				status: 200,
				body:   `{"result":"голос"}`,
			},
			audio: model.AudioInput{Data: makeWAV([]byte{0x01, 0x00, 0x02, 0x00}), MimeType: "audio/wav"},
			checkReq: func(t *testing.T, r *http.Request, body []byte) {
				t.Helper()
				if got := r.URL.Query().Get("format"); got != "lpcm" {
					t.Errorf("format query param = %q, want %q", got, "lpcm")
				}
				if got := r.URL.Query().Get("sampleRateHertz"); got != "16000" {
					t.Errorf("sampleRateHertz query param = %q, want %q", got, "16000")
				}
				want := []byte{0x01, 0x00, 0x02, 0x00}
				if string(body) != string(want) {
					t.Errorf("request body = %x, want %x (WAV header must be stripped)", body, want)
				}
			},
			wantText: "голос",
		},
		{
			name: "wav with invalid header returns error",
			response: serverResponse{
				status: 200,
				body:   `{"result":"ok"}`,
			},
			audio:          model.AudioInput{Data: []byte("not-a-wav-file"), MimeType: "audio/wav"},
			wantErr:        true,
			wantErrContain: "not a valid RIFF/WAVE file",
		},
		{
			name: "correct headers and query params",
			response: serverResponse{
				status: 200,
				body:   `{"result":"test"}`,
			},
			audio: model.AudioInput{Data: audioData, MimeType: "audio/ogg", Duration: 1 * time.Second},
			checkReq: func(t *testing.T, r *http.Request, body []byte) {
				t.Helper()
				if got := r.Header.Get("Authorization"); got != "Api-Key test-api-key" {
					t.Errorf("Authorization header = %q, want %q", got, "Api-Key test-api-key")
				}
				if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
					t.Errorf("Content-Type header = %q, want %q", got, "application/octet-stream")
				}
				if got := r.URL.Query().Get("folderId"); got != "test-folder" {
					t.Errorf("folderId query param = %q, want %q", got, "test-folder")
				}
				if got := r.URL.Query().Get("lang"); got != "ru-RU" {
					t.Errorf("lang query param = %q, want %q", got, "ru-RU")
				}
				if got := r.URL.Query().Get("format"); got != "oggopus" {
					t.Errorf("format query param = %q, want %q", got, "oggopus")
				}
			},
			wantText: "test",
		},
		{
			name: "body matches audio data",
			response: serverResponse{
				status: 200,
				body:   `{"result":"ok"}`,
			},
			audio: model.AudioInput{Data: []byte("specific-audio-bytes-12345"), MimeType: "audio/ogg"},
			checkReq: func(t *testing.T, r *http.Request, body []byte) {
				t.Helper()
				want := []byte("specific-audio-bytes-12345")
				if string(body) != string(want) {
					t.Errorf("request body = %q, want %q", string(body), string(want))
				}
			},
			wantText: "ok",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var capturedBody []byte
			var capturedReq *http.Request

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedReq = r
				var err error
				capturedBody, err = io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "read body error", 500)
					return
				}
				if tc.sleep > 0 {
					time.Sleep(tc.sleep)
				}
				w.WriteHeader(tc.response.status)
				fmt.Fprint(w, tc.response.body)
			}))
			defer srv.Close()

			cfg := config.YandexConfig{
				APIKey:   "test-api-key",
				FolderID: "test-folder",
				Language: "ru-RU",
				Endpoint: srv.URL,
			}

			httpClient := &http.Client{Timeout: 10 * time.Second}
			if tc.timeout > 0 {
				httpClient = &http.Client{Timeout: tc.timeout}
			}

			y := newYandex(cfg, httpClient)

			ctx := context.Background()
			got, err := y.Transcribe(ctx, tc.audio)

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tc.wantErrContain != "" && !strings.Contains(err.Error(), tc.wantErrContain) {
					t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantErrContain)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantText {
				t.Errorf("Transcribe() = %q, want %q", got, tc.wantText)
			}

			if tc.checkReq != nil && capturedReq != nil {
				tc.checkReq(t, capturedReq, capturedBody)
			}
		})
	}
}

func TestYandex_Transcribe_ContextTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the client's context deadline.
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"result":"hello"}`)
	}))
	defer func() {
		srv.CloseClientConnections()
		srv.Close()
	}()

	cfg := config.YandexConfig{
		APIKey:   "key",
		FolderID: "folder",
		Language: "ru-RU",
		Endpoint: srv.URL,
	}
	y := newYandex(cfg, &http.Client{Timeout: 5 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := y.Transcribe(ctx, model.AudioInput{Data: []byte("audio"), MimeType: "audio/ogg"})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") && !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context error, got: %v", err)
	}
}

func TestYandex_Transcribe_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		fmt.Fprint(w, "not json at all")
	}))
	defer srv.Close()

	cfg := config.YandexConfig{
		APIKey:   "key",
		FolderID: "folder",
		Language: "ru-RU",
		Endpoint: srv.URL,
	}
	y := newYandex(cfg, &http.Client{Timeout: 5 * time.Second})

	_, err := y.Transcribe(context.Background(), model.AudioInput{Data: []byte("audio"), MimeType: "audio/ogg"})
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Errorf("error = %q, want it to contain 'decode response'", err.Error())
	}
}

// makeWAV creates a minimal valid RIFF/WAVE file containing the given PCM payload.
func makeWAV(pcm []byte) []byte {
	dataSize := len(pcm)
	fileSize := 36 + dataSize
	out := make([]byte, 44+dataSize)
	copy(out[0:], "RIFF")
	out[4], out[5], out[6], out[7] = byte(fileSize), byte(fileSize>>8), byte(fileSize>>16), byte(fileSize>>24)
	copy(out[8:], "WAVE")
	copy(out[12:], "fmt ")
	out[16] = 16 // fmt chunk size
	out[20] = 1  // PCM format
	out[22] = 1  // mono
	// sample rate 16000 = 0x3E80
	out[24], out[25], out[26], out[27] = 0x80, 0x3E, 0x00, 0x00
	// byte rate = 32000 = 0x7D00
	out[28], out[29], out[30], out[31] = 0x00, 0x7D, 0x00, 0x00
	out[32] = 2  // block align
	out[34] = 16 // bits per sample
	copy(out[36:], "data")
	out[40], out[41], out[42], out[43] = byte(dataSize), byte(dataSize>>8), byte(dataSize>>16), byte(dataSize>>24)
	copy(out[44:], pcm)
	return out
}

func TestStripWAVHeader(t *testing.T) {
	t.Run("extracts PCM from standard 44-byte WAV", func(t *testing.T) {
		pcm := []byte{0x01, 0x00, 0x02, 0x00, 0x03, 0x00}
		wav := makeWAV(pcm)
		got, err := stripWAVHeader(wav)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != string(pcm) {
			t.Errorf("got %x, want %x", got, pcm)
		}
	})

	t.Run("rejects non-RIFF data", func(t *testing.T) {
		_, err := stripWAVHeader([]byte("not-a-wav-file-at-all"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("rejects data shorter than 12 bytes", func(t *testing.T) {
		_, err := stripWAVHeader([]byte("RIFF"))
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("rejects RIFF file without WAVE marker", func(t *testing.T) {
		data := make([]byte, 12)
		copy(data[0:], "RIFF")
		copy(data[8:], "AVI ")
		_, err := stripWAVHeader(data)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("returns error when no data chunk present", func(t *testing.T) {
		// Valid RIFF/WAVE header but only fmt chunk, no data chunk
		out := make([]byte, 28)
		copy(out[0:], "RIFF")
		out[4] = 20
		copy(out[8:], "WAVE")
		copy(out[12:], "fmt ")
		out[16] = 4 // chunk size 4 bytes
		_, err := stripWAVHeader(out)
		if err == nil {
			t.Fatal("expected error for missing data chunk, got nil")
		}
	})
}

func TestBuildYandexURL(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   string
		folderID   string
		language   string
		format     string
		wantParams map[string]string
		wantErr    bool
	}{
		{
			name:     "builds url with oggopus format",
			endpoint: "https://stt.api.cloud.yandex.net/speech/v1/stt:recognize",
			folderID: "my-folder",
			language: "ru-RU",
			format:   "oggopus",
			wantParams: map[string]string{
				"folderId": "my-folder",
				"lang":     "ru-RU",
				"format":   "oggopus",
			},
		},
		{
			name:     "builds url with mp3 format",
			endpoint: "https://stt.api.cloud.yandex.net/speech/v1/stt:recognize",
			folderID: "my-folder",
			language: "ru-RU",
			format:   "mp3",
			wantParams: map[string]string{
				"format": "mp3",
			},
		},
		{
			name:     "lpcm format adds sampleRateHertz",
			endpoint: "https://stt.api.cloud.yandex.net/speech/v1/stt:recognize",
			folderID: "my-folder",
			language: "ru-RU",
			format:   "lpcm",
			wantParams: map[string]string{
				"format":          "lpcm",
				"sampleRateHertz": "16000",
			},
		},
		{
			name:     "invalid endpoint",
			endpoint: "://invalid",
			format:   "oggopus",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := buildYandexURL(tc.endpoint, tc.folderID, tc.language, tc.format)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Parse result URL to verify params are present.
			u, err2 := url.Parse(result)
			if err2 != nil {
				t.Fatalf("result URL is not parseable: %v", err2)
			}
			for k, want := range tc.wantParams {
				if got := u.Query().Get(k); got != want {
					t.Errorf("query param %q = %q, want %q", k, got, want)
				}
			}
		})
	}
}
