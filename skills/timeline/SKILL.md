---
name: timeline
description: |
  Measure where an HTTP request's time goes (DNS, connect, TLS, server wait,
  download) with the timeline CLI. Use when a URL or API is slow, to compare a
  cold first request with warm repeats, to time a multi-step flow such as
  logging in and then fetching, or to check redirects, connection reuse and TLS.
  Response bodies come only with `--json --show-body`.
license: MIT
---

# timeline

`timeline run` sends HTTP requests in order and reports each one's phases:
blocked, dns, connect, tls, send, wait (the server's time to first byte) and
receive. Check it is installed with `timeline --version`; if not, it installs
with `go install github.com/AustinMusiku/timeline@latest`.

## Run it

```sh
timeline run https://api.example.com/health
timeline run -n 5 https://api.example.com/health https://api.example.com/items
```

Each URL gets a GET. `-n 5` runs the whole sequence five times on the same
connections, so round 1 is cold and the rest are warm. Use `-n 3` or more when
the question is "why is it slow": one round can't tell a slow handshake from a
slow server.

The output is a waterfall, one row per request with its status, URL, total
time and a bar of letters (`d` dns, `c` connect, `t` tls, `w` wait, `r`
receive). With `-n` above 1, a table follows it with each request's round 1
beside the median of its later rounds, phase by phase, in milliseconds:

```
                 total  blocked   dns  connect    tls   send   wait  receive
 1  https://api.example.com/health
    round 1      584ms    0.2ms  32ms    170ms  205ms  0.2ms  177ms    0.1ms
    median 2–5   184ms        -     -        -      -  0.1ms  184ms    0.1ms
```

Read it as: dns, connect and tls are the network and the handshake, which only
round 1 pays while the connection stays open. Wait is the server. Receive is
the size of the body and the bandwidth.

## Get numbers to compute with

`--json` writes one JSON object per line, one per request, in place of the
drawing. Every key is always present; the useful ones are `seq`, `round`,
`hop`, `url`, `status`, `error`, `conn_id`, `reused`, and `blocked_ms`,
`dns_ms`, `connect_ms`, `tls_ms`, `send_ms`, `wait_ms`, `receive_ms`,
`total_ms`.

```sh
timeline run --json -n 5 https://api.example.com/health | jq -c '{round, status, total_ms, wait_ms, error}'
```

A redirect's hops share a `seq`, with `hop` counting from 0.

Add `--show-body` to get each response's body in `body`, gunzipped, or base64
when `body_encoding` is `base64`. It needs `--json`. Use it to see what a POST
returned instead of sending it again with curl. `body_error` says why a body is
missing, for example when it is over 16 MiB.

```sh
timeline run --json --show-body -f flow.http | jq -c 'select(.status >= 400) | {name, status, body}'
```

## Requests with headers, auth and bodies

The flags are curl's: `-X`, `-H 'Name: value'`, `-u user:password`, `-d`,
`--data-raw`, `--data-binary`, `-k`, `--cacert`, `--cert`, `--key`. A command
from "Copy as cURL" runs with `curl` swapped for `timeline run`. Pass `-u` a
password: with none it asks on the terminal, and fails where there is no
terminal. Keep secrets in environment variables, as in
`-H "Authorization: Bearer $TOKEN"`.

curl flags that timeline lacks are errors that say what to use instead:
redirects are always followed (`--max-redirects` caps them), `--timeout`
replaces `-m`, `-X HEAD` replaces `-I`, `--json --show-body` replaces `-o`,
and there is no `-s`, `-v` or `-w`, since `--json` has every request's timings,
status and connection.

## Multi-step flows

When each step needs its own method, headers or body, write an `.http` file,
or pass one on stdin with `-f -`:

```sh
timeline run -n 3 -f - <<'EOF'
# @name login
POST https://api.example.com/login
Content-Type: application/json

{"user": "me", "password": "{{$processEnv PASSWORD}}"}

###
# @name me
GET https://api.example.com/me
Authorization: Bearer {{login.response.body.access_token}}
EOF
```

Requests are separated by `###`. `{{name.response.body.path}}` reads JSON from
an earlier request's response, and `{{NAME}}` an environment variable. Add
`--cookies` for APIs that keep a session in a cookie. Run
`timeline run --help` for the rest.

## Exit status

| Status     | Meaning                                                      |
| ---------- | ------------------------------------------------------------ |
| `0`        | Every request got a response. 4xx and 5xx count, so check `status` |
| `1`        | A request failed (DNS, refused, TLS, timeout); the reason is under its row and in `error` |
| `2`        | A flag, argument or file was wrong, and nothing was sent; the message says what to fix |
| `130`, `143` | Interrupted; what was collected is still printed           |

Exit 2 means no request reached the server, so fixing the command and running
it again is safe even for a POST.
