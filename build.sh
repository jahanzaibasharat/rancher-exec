#!/bin/bash

docker build -t mindk/rancher-exec-build .
docker run --rm -v ${PWD}:/tmp mindk/rancher-exec-build cp /go/bin/app /tmp/rancher-exec
