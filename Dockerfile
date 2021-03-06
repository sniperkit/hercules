FROM ubuntu:18.04

ENV GOPATH /root
ENV PROTOBUF_VERSION 3.5.1

RUN apt-get update && \
    apt-get install -y --no-install-suggests --no-install-recommends software-properties-common && \
    add-apt-repository -y ppa:gophers/archive && \
    apt-get update && \
    apt-get install -y --no-install-suggests --no-install-recommends locales golang-1.10-go python3 python3-dev libyaml-dev libyaml-0-2 libxml2-dev libxml2 curl git make unzip g++ && \
    curl -SLo protoc.zip https://github.com/google/protobuf/releases/download/v$PROTOBUF_VERSION/protoc-$PROTOBUF_VERSION-linux-x86_64.zip && \
    unzip -d /usr/local protoc.zip && rm protoc.zip && \
    locale-gen en_US.UTF-8 && \
    export PATH=/usr/lib/go-1.10/bin:$PATH && \
    go get -v -d gopkg.in/src-d/hercules.v4/... && \
    cd /root/src/gopkg.in/src-d/hercules.v4 && \
    export CGO_CXXFLAGS=-std=c++14 && \
    curl -L "https://storage.googleapis.com/tensorflow/libtensorflow/libtensorflow-cpu-$(go env GOOS)-x86_64-1.7.0.tar.gz" | tar -C /usr/local -xz && \
    make && \
    rm /usr/local/bin/protoc && rm /usr/local/readme.txt && rm -rf /usr/local/include/google && \
    cp /root/bin/hercules /usr/local/bin && \
    cp -r /root/src/gopkg.in/src-d/hercules.v4/*.py /root/src/gopkg.in/src-d/hercules.v4/internal/pb /usr/local/bin && \
    sed -i 's/parser.add_argument("--backend",/parser.add_argument("--backend", default="Agg",/' /usr/local/bin/labours.py && \
    echo '#!/bin/bash\n\
\n\
echo\n\
echo "	$@"\n\
echo\n\' > /browser && \
    chmod +x /browser && \
    curl https://bootstrap.pypa.io/get-pip.py | python3 && \
    pip3 install --no-cache-dir --no-build-isolation -r /root/src/gopkg.in/src-d/hercules.v4/requirements.txt https://github.com/mind/wheels/releases/download/tf1.7-cpu/tensorflow-1.7.0-cp36-cp36m-linux_x86_64.whl && \
    rm -rf /root/* && \
    apt-get remove -y software-properties-common golang-1.10-go python3-dev libyaml-dev libxml2-dev curl git make unzip g++ && \
    apt-get remove -y *-doc *-man && \
    rm -rf /usr/share/doc /usr/share/man && \
    apt-get autoremove -y && \
    rm -rf /var/lib/apt/lists/* && \
    apt-get clean

EXPOSE 8000
ENV BROWSER /browser
ENV LC_ALL en_US.UTF-8
ENV COUPLES_SERVER_TIME 7200
