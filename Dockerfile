FROM golang:1.25-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=0.1.0-dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build \
	-trimpath \
	-ldflags "-s -w -X main.version=${VERSION}" \
	-o /out/devrail-router \
	./cmd/devrail-router

RUN CGO_ENABLED=0 GOOS="${TARGETOS}" GOARCH="${TARGETARCH}" go build \
	-trimpath \
	-o /out/mock-openai-backend \
	./test/mock-openai-backend

FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.title="DevRail Router"
LABEL org.opencontainers.image.description="Local-first LLM routing and control plane for private AI infrastructure"
LABEL org.opencontainers.image.source="https://github.com/devrail-dev/devrail-router"
LABEL org.opencontainers.image.licenses="MIT"

COPY --from=build /out/devrail-router /usr/local/bin/devrail-router
COPY --from=build /out/mock-openai-backend /usr/local/bin/mock-openai-backend

USER nonroot:nonroot
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/devrail-router"]
CMD ["serve", "-config", "/etc/devrail/router.yaml"]
