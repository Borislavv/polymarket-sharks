# --- build stage ---
FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/watchtower ./cmd/watchtower

# --- runtime stage ---
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/watchtower /app/watchtower
COPY migrations /app/migrations
USER nonroot
ENTRYPOINT ["/app/watchtower"]
