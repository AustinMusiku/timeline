.PHONY: build test snapshot release

build:
	go build -o bin/timeline .

test:
	go test ./...

# Builds the release archives into dist/ without publishing anything.
snapshot:
	goreleaser release --snapshot --clean

# Updates CHANGELOG.md and tags the next version, or VERSION=vX.Y.Z if given.
release:
	@scripts/release.sh $(VERSION)
