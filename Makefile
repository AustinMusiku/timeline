.PHONY: build test demo snapshot release

build:
	go build -o bin/timeline .

test:
	go test ./...

# Records assets/demo.gif from assets/demo.tape with vhs, run in Docker.
demo: build
	docker run --rm -u $$(id -u):$$(id -g) -e HOME=/tmp -v $(CURDIR):/vhs ghcr.io/charmbracelet/vhs assets/demo.tape

# Builds the release archives into dist/ without publishing anything.
snapshot:
	goreleaser release --snapshot --clean

# Updates CHANGELOG.md and tags the next version, or VERSION=vX.Y.Z if given.
release:
	@scripts/release.sh $(VERSION)
