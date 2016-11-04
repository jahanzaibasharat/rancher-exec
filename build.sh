#!/bin/bash

docker run --rm -v ${PWD}:/go/src/app rancher-exec-build bash -c "go-wrapper install && cp /go/bin/app /go/src/app/rancher-exec"
