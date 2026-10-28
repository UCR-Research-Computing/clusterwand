# clusterwand hosted MCP server (clusterwand serve) for Cloud Run.
# Static Go binary on a distroless base: no shell, no package manager.
FROM golang:1.26.8 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/UCR-Research-Computing/clusterwand/internal/version.Version=${VERSION}" \
      -o /out/clusterwand ./cmd/clusterwand

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/clusterwand /clusterwand
USER nonroot:nonroot
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/clusterwand", "serve", "--config", "/config/config.yaml"]
