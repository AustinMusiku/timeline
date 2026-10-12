package render

import (
	"strings"
	"testing"
	"time"

	"github.com/AustinMusiku/timeline/internal/record"
)

func TestJSON(t *testing.T) {
	started := time.Date(2026, 10, 12, 9, 14, 3, 218417000, time.FixedZone("EAT", 3*60*60))
	tests := []struct {
		name string
		e    record.Entry
		want string
	}{
		{
			name: "cold request",
			e: record.Entry{
				Seq: 1, Round: 2, Started: started, Offset: 650*ms + 4*time.Microsecond,
				Request:  record.Request{Method: "GET", URL: "https://example.com/?a=1&b=2"},
				Response: &record.Response{Status: 200, Proto: "HTTP/2.0", BodySize: 1256},
				Timings:  record.Timings{Blocked: 112 * time.Microsecond, DNS: 12 * ms, Connect: 18 * ms, TLS: 41 * ms, Send: 83400 * time.Nanosecond, Wait: 96 * ms, Receive: 1200 * time.Microsecond},
				Conn:     record.Conn{RemoteAddr: "93.184.216.34:443"},
			},
			want: `{"seq":1,"round":2,"started_at":"2026-10-12T06:14:03.218417Z","offset_ms":650.004,` +
				`"method":"GET","url":"https://example.com/?a=1&b=2","status":200,"proto":"HTTP/2.0","body_bytes":1256,` +
				`"reused":false,"remote_addr":"93.184.216.34:443",` +
				`"blocked_ms":0.112,"dns_ms":12,"connect_ms":18,"tls_ms":41,"send_ms":0.083,"wait_ms":96,"receive_ms":1.2,` +
				`"total_ms":168.395,"error":""}`,
		},
		{
			name: "no response",
			e: record.Entry{
				Started: started,
				Request: record.Request{Method: "GET", URL: "https://nope.invalid/"},
				Timings: record.Timings{DNS: 30 * ms},
				Error:   "dial tcp: lookup nope.invalid: no such host",
			},
			want: `{"seq":0,"round":0,"started_at":"2026-10-12T06:14:03.218417Z","offset_ms":0,` +
				`"method":"GET","url":"https://nope.invalid/","status":0,"proto":"","body_bytes":0,` +
				`"reused":false,"remote_addr":"",` +
				`"blocked_ms":0,"dns_ms":30,"connect_ms":0,"tls_ms":0,"send_ms":0,"wait_ms":0,"receive_ms":0,` +
				`"total_ms":30,"error":"dial tcp: lookup nope.invalid: no such host"}`,
		},
	}
	for _, tt := range tests {
		var b strings.Builder
		if err := JSON(&b, tt.e); err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if got := b.String(); got != tt.want+"\n" {
			t.Errorf("%s:\n got %s\nwant %s", tt.name, got, tt.want)
		}
	}
}
