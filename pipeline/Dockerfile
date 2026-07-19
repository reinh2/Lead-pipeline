FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -o /out/pipeline ./cmd/pipeline

FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget \
    && addgroup -S pipeline \
    && adduser -S -G pipeline -h /nonexistent -s /sbin/nologin pipeline \
    && mkdir -p /data \
    && chown pipeline:pipeline /data
COPY --from=build /out/pipeline /usr/local/bin/pipeline
ENV PIPELINE_PORT=8081 CSV_PATH=/data/leads.csv
EXPOSE 8081
USER pipeline
ENTRYPOINT ["/usr/local/bin/pipeline"]
