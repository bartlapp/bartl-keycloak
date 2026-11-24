FROM --platform=linux/amd64 golang:1.25.3@sha256:6d4e5e74f47db00f7f24da5f53c1b4198ae46862a47395e30477365458347bf2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Pinned collector source: https://github.com/google/go-licenses/tree/v2.0.1
RUN go install github.com/google/go-licenses/v2@v2.0.1
COPY LICENSE NOTICE ./
COPY internal/native/ internal/native/
COPY cmd/backup/ cmd/backup/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/backup ./cmd/backup
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go-licenses save ./cmd/backup --save_path=/out/licenses/go \
    && mkdir -p /out/licenses/golang \
    && cp /usr/local/go/LICENSE /usr/local/go/PATENTS /out/licenses/golang/
RUN apt-get update && apt-get install -y --no-install-recommends curl bzip2 \
    && rm -rf /var/lib/apt/lists/*
# Official v0.19.1 release SHA256SUMS: https://github.com/restic/restic/releases/download/v0.19.1/SHA256SUMS
RUN curl --fail --location --silent --show-error https://github.com/restic/restic/releases/download/v0.19.1/restic_0.19.1_linux_amd64.bz2 -o /tmp/restic.bz2 \
    && echo 'f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c  /tmp/restic.bz2' | sha256sum -c - \
    && bzip2 -dc /tmp/restic.bz2 > /out/restic \
    && chmod 0555 /out/restic /out/backup
# Collect notices from the matching upstream source, with its release build tags.
# This does not rebuild or replace the checksum-verified release binary above.
# Blazer's Apache notice and Fuse's composite BSD/ISC text are retained verbatim
# below because the collector cannot classify those two upstream LICENSE files.
RUN export GOTOOLCHAIN=go1.25.10 \
    && go mod download github.com/restic/restic@v0.19.1 \
    && cd /go/pkg/mod/github.com/restic/restic@v0.19.1 \
    && export GOROOT="$(go env GOROOT)" \
    && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOFLAGS="-tags=selfupdate,disable_grpc_modules" \
       go-licenses save ./cmd/restic --save_path=/out/licenses/restic \
       --ignore=github.com/Backblaze/blazer,github.com/anacrolix/fuse \
    && mkdir -p /out/licenses/restic/github.com/Backblaze/blazer /out/licenses/restic/github.com/anacrolix/fuse \
    && cp "$(go list -m -f '{{.Dir}}' github.com/Backblaze/blazer)/LICENSE" /out/licenses/restic/github.com/Backblaze/blazer/ \
    && cp "$(go list -m -f '{{.Dir}}' github.com/anacrolix/fuse)/LICENSE" /out/licenses/restic/github.com/anacrolix/fuse/ \
    && mkdir -p /out/licenses/restic/golang \
    && cp "$(go env GOROOT)/LICENSE" "$(go env GOROOT)/PATENTS" /out/licenses/restic/golang/ \
    && go version -m /out/restic > /out/licenses/restic-build.txt

FROM --platform=linux/amd64 docker.io/library/postgres:17.6@sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929
LABEL org.opencontainers.image.licenses="Apache-2.0"
COPY LICENSE NOTICE /usr/share/licenses/bartl-keycloak/
COPY --from=build /out/licenses/ /usr/share/licenses/bartl-keycloak/
COPY --from=build --chown=65532:65532 /out/backup /backup
COPY --from=build --chown=65532:65532 /out/restic /usr/local/bin/restic
ENV HOME=/tmp TMPDIR=/tmp
USER 65532:65532
ENTRYPOINT ["/backup"]
