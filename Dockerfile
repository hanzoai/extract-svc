# Stage 1 — static go build. No CGo, no Node, no Python — every
# format extractor is pure Go.
FROM golang:1.26.5-alpine AS build

RUN apk add --no-cache ca-certificates tzdata
RUN addgroup -g 65532 -S nonroot && adduser -u 65532 -S nonroot -G nonroot

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG COMMIT_HASH=dev
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -trimpath -ldflags="-s -w -X main.Version=${COMMIT_HASH}" \
    -o /extract ./cmd/extract

# Stage 2 — scratch runtime. Final image is the binary + minimal
# filesystem (CA certs, tzdata, passwd/group). ~15 MB.
FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/passwd /etc/passwd
COPY --from=build /etc/group /etc/group

WORKDIR /app
COPY --from=build /extract /app/extract

EXPOSE 4004
USER 65532:65532
ENTRYPOINT ["/app/extract"]
