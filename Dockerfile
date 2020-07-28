# Build layer
FROM golang:1.14 AS builder
WORKDIR /go/src/github.com/seatgeek/resec
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-X 'main.Version=${DOCKER_TAG}'" -a -installsuffix cgo -o build/resec  .

# Run layer
FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /go/src/github.com/seatgeek/resec/build/resec .
CMD ["./resec"]
