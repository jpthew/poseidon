FROM itsafeaturemythic/mythic_go_macos:latest

USER root

RUN apt-get update && apt-get install -y \
    clang \
    llvm \
    lld \
    curl \
    xz-utils \
    make \
    git \
    libssl-dev \
    && rm -rf /var/lib/apt/lists/*

ENV SDK_DIR=/opt/sdk/iPhoneOS16.5.sdk
RUN mkdir -p /opt/sdk && \
    curl -Lo /tmp/sdk.tar.xz https://github.com/theos/sdks/releases/download/master-146e41f/iPhoneOS16.5.sdk.tar.xz && \
    tar -xvf /tmp/sdk.tar.xz -C /opt/sdk/ && \
    rm /tmp/sdk.tar.xz

RUN echo '#!/bin/bash\nclang -target arm64-apple-ios16.5 -isysroot /opt/sdk/iPhoneOS16.5.sdk "$@"' > /usr/local/bin/aarch64-apple-ios16.5-clang && \
    echo '#!/bin/bash\nclang -target arm64-apple-ios16.5-simulator -isysroot /opt/sdk/iPhoneOS16.5.sdk "$@"' > /usr/local/bin/aarch64-apple-ios16.5-simulator-clang && \
    chmod +x /usr/local/bin/aarch64-apple-ios16.5-clang /usr/local/bin/aarch64-apple-ios16.5-simulator-clang

RUN chmod +x /go/bin/garble
RUN garble version

WORKDIR /Mythic/
COPY [".", "."]

ENV CGO_ENABLED=1
RUN make build

CMD ["make", "run"]