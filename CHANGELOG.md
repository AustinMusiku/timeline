# Changelog

Notable changes to timeline. Versions follow [Semantic Versioning](https://semver.org).

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
