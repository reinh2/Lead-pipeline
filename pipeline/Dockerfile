FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -o /out/pipeline ./cmd/pipeline

FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget && mkdir -p /data
COPY --from=build /out/pipeline /usr/local/bin/pipeline
ENV PIPELINE_PORT=8081 CSV_PATH=/data/leads.csv
EXPOSE 8081
ENTRYPOINT ["/usr/local/bin/pipeline"]
