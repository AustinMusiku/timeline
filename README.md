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

| Flag                  | Default | What it does                                    |
| --------------------- | ------- | ----------------------------------------------- |
| `-n`, `--repeat`      | `1`     | Times to run the whole sequence                 |
| `-c`, `--concurrency` | `1`     | Requests to keep in flight at once              |
| `--timeout`           | `30s`   | Time limit for each request, `0` for none       |
| `--json`              | off     | One line of JSON per request instead of drawing |
| `--color`             | `auto`  | `auto`, `always` or `never`                     |

`timeline run --help` has the full reference.

## Reading the waterfall

```
                                         0              1s              2s
                                         ┬──────────────┬───────────────┬───────
 1  404  https://go.dev/nope      1.62s  ccccccctttttttttwwwwwwwwwr
 2  ERR  http://nosuch.invalid     42ms                           dd
         dial tcp: lookup nosuch.invalid: no such host

 1  404  https://go.dev/nope      799ms                            wwwwwwwwwwwrr
 2  ERR  http://nosuch.invalid     16ms                                        d
         dial tcp: lookup nosuch.invalid: no such host

 - blocked  d dns  c connect  t tls  s send  w wait  r receive
```

A row is the request's place in the sequence, its status, its URL, how long it
took, and its bar. A bar starts where the request started and is made of these
phases, in order:

| Phase   | Letter | Colour  | What it covers                                   |
| ------- | ------ | ------- | ------------------------------------------------ |
| blocked | `-`    | grey    | Before any network activity, such as waiting for a free connection |
| dns     | `d`    | cyan    | Looking up the host                              |
| connect | `c`    | yellow  | The TCP handshake                                |
| tls     | `t`    | magenta | The TLS handshake                                |
| send    | `s`    | default | Writing the request                              |
| wait    | `w`    | green   | Request written, waiting for the first byte back |
| receive | `r`    | blue    | First byte to the end of the body                |

In a terminal the bars are coloured blocks. Piped, or with `--color never`,
they are the letters above.

A blank line separates rounds of `--repeat`. Connections stay open between
rounds, so the first round is cold and the rest are warm: a warm request has no
dns, connect or tls.

A request that got no response shows `ERR` with the reason underneath, and its
bar stops in the phase that failed. A 4xx or 5xx is a response like any other.

Redirects are not followed, so a 301 is drawn as a 301.

## JSON

```sh
timeline run --json -n 5 https://example.com | jq -s 'map(.wait_ms) | add / length'
```

`--json` writes one object per request, as each one finishes:

```json
{
  "seq":0,
  "round":0,
  "started_at":"2026-10-12T09:32:53.918805Z",
  "offset_ms":0.279,
  "method":"GET",
  "url":"https://go.dev",
  "status":200,
  "proto":"HTTP/2.0",
  "body_bytes":12085,
  "reused":false,
  "remote_addr":"216.239.36.21:443",
  "blocked_ms":0.774,
  "dns_ms":149.767,
  "connect_ms":536.01,
  "tls_ms":347.228,
  "send_ms":0.578,
  "wait_ms":498.204,
  "receive_ms":192.256,
  "total_ms":1724.816,
  "error":""
}
```

Every key is always present. `timeline run --help` describes the format.

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

`--config` points at a different file. `NO_COLOR` is honoured.

## Exit status

`timeline` exits 1 when any request got no response or the run was interrupted,
and 0 otherwise. Ctrl-C still draws what was collected.

## Not yet

- Methods other than GET, headers and request bodies
- HAR files as input
- Windows builds

## Development

```sh
make test      # go test ./...
make build     # bin/timeline
make demo      # re-record assets/demo.gif
make release   # update CHANGELOG.md and tag the next version
```

## License

MIT. See [LICENSE](LICENSE).
