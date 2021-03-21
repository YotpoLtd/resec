# Build layer
FROM golang:1.16 AS builder
WORKDIR /go/src/github.com/seatgeek/resec
COPY . .
ARG RESEC_VERSION
ENV RESEC_VERSION ${RESEC_VERSION:-local-dev}
RUN echo $RESEC_VERSION
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-X 'main.Version=${RESEC_VERSION}'" -a -installsuffix cgo -o build/resec  .

# Run layer
FROM alpine:latest
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=builder /go/src/github.com/seatgeek/resec/build/resec .
CMD ["./resec"]
