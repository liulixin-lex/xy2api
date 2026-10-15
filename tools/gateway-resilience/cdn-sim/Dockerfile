FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /cdn-sim .

FROM alpine:3.20
COPY --from=build /cdn-sim /usr/local/bin/cdn-sim
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cdn-sim"]
