# Build Golang
FROM docker.io/golang:alpine3.15 AS builder
WORKDIR /opt
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /opt/app .
FROM debian:10.10-slim
WORKDIR /opt
ARG COMMIT_ARG
ENV COMMIT_ID $COMMIT_ARG
RUN echo $COMMIT_ID > /opt/git_commit
COPY --from=builder /opt/app /opt/app
COPY --from=builder /opt/jiralert.yml /opt/jiralert.yml
COPY --from=builder /opt/jiralert.tmpl /opt/jiralert.tmpl
CMD ["/opt/app","ska"]
