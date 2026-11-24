FROM golang:1.25.3@sha256:6d4e5e74f47db00f7f24da5f53c1b4198ae46862a47395e30477365458347bf2 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
# Pinned collector source: https://github.com/google/go-licenses/tree/v2.0.1
RUN go install github.com/google/go-licenses/v2@v2.0.1
COPY LICENSE NOTICE ./
COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/controller ./cmd/controller
RUN CGO_ENABLED=0 go-licenses save ./cmd/controller --save_path=/out/licenses/go \
    && mkdir -p /out/licenses/golang \
    && cp /usr/local/go/LICENSE /usr/local/go/PATENTS /out/licenses/golang/

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
LABEL org.opencontainers.image.licenses="Apache-2.0"
COPY LICENSE NOTICE /usr/share/licenses/bartl-keycloak/
COPY --from=build /out/licenses/ /usr/share/licenses/bartl-keycloak/
COPY --from=build --chown=65532:65532 /out/controller /controller
USER 65532:65532
ENTRYPOINT ["/controller"]
