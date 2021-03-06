# Build
FROM golang:1.13rc2 as builder
#
# protoc
ENV PROTOC_VERSION 3.9.0
RUN apt-get update && \
    apt-get upgrade -y -o Dpkg::Options::="--force-confold" && \
    apt-get install -y unzip
RUN wget https://github.com/google/protobuf/releases/download/v$PROTOC_VERSION/protoc-$PROTOC_VERSION-linux-x86_64.zip && \
   unzip protoc-$PROTOC_VERSION-linux-x86_64.zip && \
   rm -f protoc-$PROTOC_VERSION-linux-x86_64.zip
#
# protoc-gen-go
ENV GOLANG_PROTOBUF_VERSION v1.2.0
RUN mkdir -p /go/src/github.com/golang && \
   cd /go/src/github.com/golang && \
   git clone https://github.com/golang/protobuf.git && \
   cd protobuf && \
   git checkout $GOLANG_PROTOBUF_VERSION && \
   cd protoc-gen-go && \
   go install
#
# grpc-gateway & swagger
ENV GRPC_GATEWAY_VERSION v1.11.0
RUN git clone https://github.com/grpc-ecosystem/grpc-gateway.git && \
    cd grpc-gateway && \
    git checkout $GRPC_GATEWAY_VERSION && \
    cd protoc-gen-grpc-gateway && \
    go install && \
    cd ../protoc-gen-swagger && \
    go install
#
# micro
ENV MICRO_VERSION v0.8.0
RUN git clone https://github.com/micro/protoc-gen-micro.git && \
    cd protoc-gen-micro && \
    git checkout $MICRO_VERSION && \
    go install
#
# validate
ENV VALIDATE_VERSION v0.1.0
RUN git clone https://github.com/envoyproxy/protoc-gen-validate.git && \
    cd protoc-gen-validate && \
    git checkout $VALIDATE_VERSION && \
    go mod init github.com/envoyproxy/protoc-gen-validate && \
    go install
#
# godna
ENV GODNA_VER v1.14.0
RUN git clone https://github.com/wxio/godna.git  && \
    cd godna && \
    git checkout $GODNA_VER && \
    go install -ldflags  "-X main.version=$GODNA_VER -X main.commit=$(git log --pretty=format:\"%H\" -1) -X main.date=$(git log --pretty=format:\"%ad\" -1 --date=format:'%F-%T%z')"

# Package
FROM golang:1.13rc2
#
COPY --from=builder /go/bin/* /go/bin/
COPY --from=builder /go/include /go/include
# GOPRIVATE="bitbucket.org"

RUN echo 'complete -C /go/bin/godna godna' >> /etc/bash.bashrc

RUN echo '[ ! -z "$TERM" -a -r /etc/motd ] && cat /etc/motd' >> /etc/bash.bashrc
COPY motd /etc/motd
ADD entrypoint.sh /entrypoint.sh
ADD generate_allsteps.sh /generate_allsteps.sh

# ENTRYPOINT [ "/entrypoint.sh" ]
CMD [ "/generate_allsteps.sh" ]