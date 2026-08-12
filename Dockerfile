# Two-stage build for jtrax-backend. Every dependency is pure Go — including
# the SQLite and libSQL drivers — so CGO stays off and the result is a static
# binary on a distroless base with no shell and no package manager.

FROM golang:1.25-alpine AS build
WORKDIR /src

# Dependencies first so edits to the source do not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /app/server

# Render overrides this with its own PORT; the default keeps `docker run` usable.
ENV PORT=8080
EXPOSE 8080

USER nonroot:nonroot
ENTRYPOINT ["/app/server"]
