package pushproto

const (
	StatusSent          = "sent"
	StatusInvalidToken  = "invalid_token"
	StatusUpstreamError = "upstream_error"
	StatusBadPayload    = "bad_payload"
)

// PushRequest is the body of POST /v1/push (instance → relay).
type PushRequest struct {
	DeviceID     string       `json:"deviceId"`
	Priority     string       `json:"priority"`
	CollapseKey  string       `json:"collapseKey,omitempty"`
	Notification Notification `json:"notification"`
	Data         Data         `json:"data,omitempty"`
}

// Notification holds the title and body of a push notification.
type Notification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Badge *int   `json:"badge,omitempty"`
}

// Data carries machine-readable metadata for the client.
type Data struct {
	Kind      string `json:"kind"`
	EventSeq  int64  `json:"eventSeq"`
	TaskID    string `json:"taskId,omitempty"`
	DisplayID string `json:"displayId,omitempty"`
}

// PushResponse is the relay's response to POST /v1/push.
type PushResponse struct {
	Status     string `json:"status"`
	RetryAfter int    `json:"retryAfter,omitempty"`
	Message    string `json:"message,omitempty"`
}

// RegistrationRequest is the body of PUT /v1/registrations/{device_id}.
type RegistrationRequest struct {
	APNSToken *string `json:"apnsToken,omitempty"`
	FCMToken  *string `json:"fcmToken,omitempty"`
	Platform  string  `json:"platform"`
}
