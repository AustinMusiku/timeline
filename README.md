# timeline

A network waterfall for the terminal. `timeline` sends a sequence of HTTP
requests and draws them all on one time axis, with each bar split into DNS,
connect, TLS, wait and download.

![timeline drawing three requests, cold then warm](assets/demo.gif)

## Install

With Go 1.27 or newer:

```sh
go install github.com/AustinMusiku/timeline@latest
```

Or download an archive for Linux or macOS (amd64 and arm64) from the
[releases page](https://github.com/AustinMusiku/timeline/releases) and put the
`timeline` binary on your `PATH`.

## Usage

```sh
timeline run https://example.com https://example.com/about
```

Each URL gets a GET, one after the other, in the order given.

| Flag                  | Default | What it does                                         |
| --------------------- | ------- | ---------------------------------------------------- |
| `-n`, `--repeat`      | `1`     | Times to run the whole sequence                      |
| `-c`, `--concurrency` | `1`     | Requests to keep in flight at once                   |
| `--timeout`           | `30s`   | Time limit for each request, `0` for none            |
| `--max-redirects`     | `10`    | Redirects to follow from each request, `0` for none  |
| `-X`, `--method`      | `GET`   | Method for every request, POST when there is a body  |
| `-H`, `--header`      |         | `Name: value` to send with every request, repeatable |
| `-u`, `--user`        |         | `user:password` for basic auth on every request      |
| `-d`, `--data`        |         | Body for every request, `@file` or `@-` for stdin    |
| `--data-raw`          |         | Like `-d`, but a leading `@` is sent as typed        |
| `--data-binary`       |         | Like `-d`, but `@file` keeps its line breaks         |
| `--json`              | off     | One line of JSON per request instead of drawing      |
| `--color`             | `auto`  | `auto`, `always` or `never`                          |

`timeline run --help` has the full reference.

### Authentication

```sh
timeline run -H "Authorization: Bearer $TOKEN" https://api.example.com/me
timeline run -u me https://api.example.com/me
```

`-u` with no password asks for one, so it stays out of your shell history. An
`Authorization` from `-H` wins over `-u`. Credentials are not sent on to
another host when a redirect leads there.

Output never shows credentials: a password in a URL becomes `xxxxx`, an
`Authorization` keeps only its scheme (`Bearer xxxxx`), and a `Cookie` only its
names (`session=xxxxx`).

### Request bodies

```sh
timeline run -d 'user=me&remember=1' https://example.com/login
timeline run -H 'Content-Type: application/json' -d @body.json https://api.example.com/items
```

The body flags work as in curl, so a command from your browser's "Copy as
cURL" runs with `curl` swapped for `timeline run`:

- A body makes the request a POST, unless `-X` says otherwise, and sends
  `Content-Type: application/x-www-form-urlencoded`, unless `-H` gives one.
- `-d @file` reads a file and drops its line breaks, which suits form data.
  `--data-binary @file` sends the file exactly as it is, and `--data-raw`
  never reads a file.
- Several body flags are joined with `&`, in the order given.

Each repeat sends the same bytes. A 307 or 308 redirect sends them on again,
and a 301, 302 or 303 turns the request into a GET with no body.

Writing the body is part of send. A small body is handed to the kernel in one
go, so send stays short and its trip to the server shows up in wait; send
grows only once the body outgrows the socket's buffer.

## Reading the waterfall

```
                                             0              500ms           1s
                                             ┬──────────────┬───────────────┬───
 1  404  #1  https://go.dev/nope      745ms  ddcccccttttttwwwwwwwwwwr
 2  ERR      http://nosuch.invalid     51ms                         dd
             dial tcp: lookup nosuch.invalid: no such host

 1  404  #1  https://go.dev/nope      312ms                          wwwwwwwwwwr
 2  ERR      http://nosuch.invalid     18ms                                    d
             dial tcp: lookup nosuch.invalid: no such host

 #1  216.239.34.21:443  HTTP/2.0  TLS 1.3

 - blocked  d dns  c connect  t tls  s send  w wait  r receive
```

A row is the request's place in the sequence, its status, the connection it
used, its URL, how long it took, and its bar. A bar starts where the request
started and is made of these phases, in order:

| Phase   | Letter | Colour  | What it covers                                   |
| ------- | ------ | ------- | ------------------------------------------------ |
| blocked | `-`    | grey    | Before any network activity, such as waiting for a free connection |
| dns     | `d`    | cyan    | Looking up the host                              |
| connect | `c`    | yellow  | The TCP handshake                                |
| tls     | `t`    | magenta | The TLS handshake                                |
| send    | `s`    | default | Writing the request and its body                 |
| wait    | `w`    | green   | Request written, waiting for the first byte back |
| receive | `r`    | blue    | First byte to the end of the body                |

In a terminal the bars are coloured blocks. Piped, or with `--color never`,
they are the letters above.

A blank line separates rounds of `--repeat`. Connections stay open between
rounds, so the first round is cold and the rest are warm: a warm request has no
dns, connect or tls.

Connections are numbered in the order the run first used them, and listed under
the chart with their address, protocol and TLS version. Rows with the same `#`
shared a connection: several at once under HTTP/2, one after another under
HTTP/1.1. A connection marked `resumed` was opened with a shortened TLS
handshake, using a session from an earlier connection in the run.

A request that got no response shows `ERR` with the reason underneath, and its
bar stops in the phase that failed. A 4xx or 5xx is a response like any other.

Redirects are followed, and each hop is a request of its own, drawn on a row
under the one that led to it:

```
 1  302  #1  http://go.dev             523ms  ddwwww
    200  #2  ↳ https://go.dev/         809ms       ccttwwwr
```

After `--max-redirects` hops the last redirect is left unfollowed, with
`stopped after 10 redirects` under its row, and counts as a failed request.

## JSON

```sh
timeline run --json -n 5 https://example.com | jq -s 'map(.wait_ms) | add / length'
```

`--json` writes one object per request, as each one finishes:

```json
{
  "seq":0,
  "round":0,
  "hop":0,
  "started_at":"2026-10-12T14:01:33.823986Z",
  "offset_ms":0.118,
  "method":"GET",
  "url":"https://go.dev",
  "request_headers":{"Accept-Encoding":["gzip"]},
  "request_body_bytes":0,
  "status":200,
  "proto":"HTTP/2.0",
  "body_bytes":12085,
  "location":null,
  "conn_id":1,
  "reused":false,
  "remote_addr":"216.239.34.21:443",
  "local_addr":"172.29.91.41:41948",
  "tls_version":"TLS 1.3",
  "tls_resumed":false,
  "alpn":"h2",
  "attempts":1,
  "resolved_ips":[
    "216.239.34.21","216.239.32.21","216.239.38.21","216.239.36.21",
    "2001:4860:4802:34::15","2001:4860:4802:32::15",
    "2001:4860:4802:38::15","2001:4860:4802:36::15"
  ],
  "dial_attempts":1,
  "blocked_ms":0.234,
  "dns_ms":75.149,
  "connect_ms":153.612,
  "tls_ms":203.429,
  "send_ms":0.132,
  "wait_ms":315.502,
  "receive_ms":34.016,
  "total_ms":782.074,
  "error":""
}
```

Every key is always present. The hops of a redirect share a `seq`, and `hop`
counts them from 0 for the URL that was given. `request_headers` are the
headers timeline set, with credentials hidden; Go's client adds `Host`,
`User-Agent` and the like as it sends. `request_body_bytes` is the size of the
body sent; the body itself is never written out. `location` is the response's
`Location` header, or `null` if it had none. `conn_id` is the number the
waterfall shows, and 0 for a request that never got a connection.
`resolved_ips` and `dial_attempts` describe the request's own dial, so they are
empty and 0 on a reused connection. `timeline run --help` describes the format.

## Configuration

A setting is read from its flag, then a `TIMELINE_*` environment variable, then
the config file, then the default.

```sh
TIMELINE_REPEAT=3 timeline run https://example.com
```

```yaml
# ~/.config/timeline/config.yaml
# (~/Library/Application Support/timeline/config.yaml on macOS)
repeat: 3
timeout: 5s
color: never
```

`--config` points at a different file. `NO_COLOR` is honoured. `-H`, `-u` and
the body flags can carry credentials, so they are read from the command line
only.

## Exit status

`timeline` exits 1 when any request got no response or the run was interrupted,
and 0 otherwise. Ctrl-C still draws what was collected.

## Development

```sh
make test      # go test ./...
make build     # bin/timeline
make demo      # re-record assets/demo.gif
make release   # update CHANGELOG.md and tag the next version
```

## License

MIT. See [LICENSE](LICENSE).
