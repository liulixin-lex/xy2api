FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /fault-upstream .

FROM alpine:3.20
COPY --from=build /fault-upstream /usr/local/bin/fault-upstream
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/fault-upstream"]
