# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27@sha256:f20d83a83e18dbd2a4714b8ffd4b30b287b6243962dd75608e86abcc63ba4efc AS build
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/coachd ./cmd/coachd \
 && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
# A fresh named volume mounted at /data inherits this ownership.
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build /out/coachd /coachd
USER 65532:65532
ENTRYPOINT ["/coachd"]
CMD ["serve"]
