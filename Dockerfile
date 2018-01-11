FROM golang:1.9.2
WORKDIR /go/src/github.com/YotpoLtd/resec/
COPY . /go/src/github.com/YotpoLtd/resec/
RUN go get ./...  
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o resec  .

FROM alpine:latest  
RUN apk --no-cache add ca-certificates
WORKDIR /root/
COPY --from=0 /go/src/github.com/YotpoLtd/resec/resec .
CMD ["./resec"]  