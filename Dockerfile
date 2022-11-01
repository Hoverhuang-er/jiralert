# Build Golang
FROM docker.io/golang:alpine3.15 AS builder
WORKDIR /opt
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /opt/app .
RUN echo "Build Complete"
FROM debian:10.10-slim
WORKDIR /opt
COPY --from=builder /opt/app /opt/app
COPY --from=builder /opt/jiralert.yaml /opt/jiralert.yaml
COPY --from=builder /opt/jiralert.tmpl /opt/jiralert.tmpl
CMD ["/opt/app","ska"]
