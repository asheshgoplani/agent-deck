FROM golang:1.25
RUN apt-get update && apt-get install -y --no-install-recommends tmux && apt-get clean
WORKDIR /src
COPY go.mod go.sum ./
ENV GOTOOLCHAIN=local GOMODCACHE=/opt/gomod
RUN go mod download && chmod -R a+rwX /opt/gomod
