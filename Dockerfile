# Stage 1 — static go build. No CGo, no Node, no Python — every
# format extractor is pure Go.
FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG COMMIT_HASH=dev
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -trimpath -ldflags="-s -w -X main.Version=${COMMIT_HASH}" \
    -o /extract ./cmd/extract

# Stage 2 — distroless static. Final image is ~15 MB.
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /extract /app/extract

EXPOSE 4004
USER nonroot:nonroot
ENTRYPOINT ["/app/extract"]
