FROM golang:1.7

WORKDIR /go/src/app

COPY . /go/src/app

RUN set -xe \
    && go-wrapper download
