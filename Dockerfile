FROM alpine:latest

COPY logging /go/bin/
RUN mkdir /log
WORKDIR /go/bin
CMD ["./logging"]
