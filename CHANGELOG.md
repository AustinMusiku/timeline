# Changelog

Notable changes to timeline. Versions follow [Semantic Versioning](https://semver.org).

## [0.11.0] - 2026-11-10

### Features

- Read response bodies when asked
- Add --show-body to write bodies in the JSON
- Point -o and -i at --show-body

### Fixes

- Keep {{variables}} whole in the request line

## [0.10.0] - 2026-11-07

### Features

- Draw whole URLs when the output is piped
- Sum up the rounds under the chart

## [0.9.0] - 2026-11-06

### Breaking

- Exit 2 when nothing was sent. A bad flag, argument or config file used to exit 1.
- Exit 130 or 143 when interrupted
- Answer curl-only flags with what timeline does instead

### Fixes

- Say a request timed out or was interrupted
- Say what a flag wants when its value is wrong
- Name the flag when its file can't be read
- Give the earlier request's status when a value is missing
- Say a URL needs http:// or https:// in front
- Say a URL has no host instead of no scheme

## [0.8.0] - 2026-11-05

### Features

- Build a step from earlier responses
- Read values out of a response
- Fill in values from earlier responses in sequence files
- Keep cookies between requests with --cookies

## [0.7.0] - 2026-10-30

### Features

- Let the collector take a TLS config
- Add -k/--insecure
- Add --cacert
- Add --cert and --key for client certificates
- Carry a request's name through to the record
- Parse .http sequence files
- Fill in {{variables}} in sequence files
- Add -f/--file to run

### Fixes

- Reject @ lines after the request line

## [0.6.0] - 2026-10-28

### Features

- Send request bodies from the collector
- Add request_body_bytes to the JSON
- Add -d/--data to run
- Add --data-raw and --data-binary

## [0.5.0] - 2026-10-23

### Features

- Add -H/--header to run
- Add request_headers to the JSON
- Add -X/--method to run
- Add -u/--user for basic auth
- Prompt for the password when -u has none

### Fixes

- Hide passwords in URLs

## [0.4.0] - 2026-10-14

### Features

- Follow redirects in the collector
- Draw redirect hops and add hop and location to the JSON
- **Breaking:** Follow up to 10 redirects by default

## [0.3.0] - 2026-10-13

### Features

- Number connections within a run
- Record TLS version, ALPN and resumption
- Record resolved IPs
- Record attempts and the local address
- Add connection fields to JSON output
- Show connections in the waterfall

## [0.2.0] - 2026-10-12

### Features

- Add OnEntry hook to the collector
- Add JSON line encoder
- Add --json flag to run

### Fixes

- Stop cleanly on SIGTERM

## [0.1.0] - 2026-10-11

Initial release.
