package render

import (
	"encoding/json"
	"io"
	"time"

	"github.com/AustinMusiku/timeline/internal/record"
)

// jsonEntry is one line of JSON output. Every key is always present, so a
// phase that did not happen is zero, a request with no response has status 0
// and one with no connection has conn_id 0.
type jsonEntry struct {
	Seq          int      `json:"seq"`
	Round        int      `json:"round"`
	StartedAt    string   `json:"started_at"`
	OffsetMS     float64  `json:"offset_ms"`
	Method       string   `json:"method"`
	URL          string   `json:"url"`
	Status       int      `json:"status"`
	Proto        string   `json:"proto"`
	BodyBytes    int64    `json:"body_bytes"`
	ConnID       int      `json:"conn_id"`
	Reused       bool     `json:"reused"`
	RemoteAddr   string   `json:"remote_addr"`
	LocalAddr    string   `json:"local_addr"`
	TLSVersion   string   `json:"tls_version"`
	TLSResumed   bool     `json:"tls_resumed"`
	ALPN         string   `json:"alpn"`
	Attempts     int      `json:"attempts"`
	ResolvedIPs  []string `json:"resolved_ips"`
	DialAttempts int      `json:"dial_attempts"`
	BlockedMS    float64  `json:"blocked_ms"`
	DNSMS        float64  `json:"dns_ms"`
	ConnectMS    float64  `json:"connect_ms"`
	TLSMS        float64  `json:"tls_ms"`
	SendMS       float64  `json:"send_ms"`
	WaitMS       float64  `json:"wait_ms"`
	ReceiveMS    float64  `json:"receive_ms"`
	TotalMS      float64  `json:"total_ms"`
	Error        string   `json:"error"`
}

// JSON writes e as one line of JSON, in a single write.
func JSON(w io.Writer, e record.Entry) error {
	t, c := e.Timings, e.Conn
	line := jsonEntry{
		Seq:          e.Seq,
		Round:        e.Round,
		StartedAt:    e.Started.UTC().Format("2006-01-02T15:04:05.000000Z"),
		OffsetMS:     millis(e.Offset),
		Method:       e.Request.Method,
		URL:          e.Request.URL,
		ConnID:       c.ID,
		Reused:       c.Reused,
		RemoteAddr:   c.RemoteAddr,
		LocalAddr:    c.LocalAddr,
		TLSVersion:   c.TLSVersion,
		TLSResumed:   c.TLSResumed,
		ALPN:         c.ALPN,
		Attempts:     e.Attempts,
		ResolvedIPs:  append([]string{}, e.Dial.Resolved...), // [] when empty, not null
		DialAttempts: e.Dial.Connects,
		BlockedMS:    millis(t.Blocked),
		DNSMS:        millis(t.DNS),
		ConnectMS:    millis(t.Connect),
		TLSMS:        millis(t.TLS),
		SendMS:       millis(t.Send),
		WaitMS:       millis(t.Wait),
		ReceiveMS:    millis(t.Receive),
		TotalMS:      millis(t.Total()),
		Error:        e.Error,
	}
	if r := e.Response; r != nil {
		line.Status, line.Proto, line.BodyBytes = r.Status, r.Proto, r.BodySize
	}

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false) // leave the & in a query string alone
	return enc.Encode(line)
}

// millis is d in milliseconds, rounded to the microsecond.
func millis(d time.Duration) float64 {
	return float64(d.Round(time.Microsecond).Microseconds()) / 1000
}
