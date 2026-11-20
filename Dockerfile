# syntax=docker/dockerfile:1
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY web ./web
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/pod-manager .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pod-manager /pod-manager
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/pod-manager"]
