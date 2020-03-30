FROM golang:1.13.8-buster as builder

# Gather the librdkafka and knative-kafka-dispatcher dependencies for building
RUN wget -qO - https://packages.confluent.io/deb/5.3/archive.key | apt-key add - \
    && echo "deb https://packages.confluent.io/deb/5.3 stable main" >> /etc/apt/sources.list \
    && echo "deb http://security.debian.org/debian-security jessie/updates main" >> /etc/apt/sources.list \
    && apt-get update \
    && apt-get -y install librdkafka-dev


# Get The Microsoft Cert For SSL
WORKDIR /tmp-certs
RUN curl -sSL -f -k http://www.microsoft.com/pki/mscorp/Microsoft%20IT%20TLS%20CA%202.crt -o /tmp-certs/microsoft.crt

# Determine Dependencies of knative-kafka-controller that will be needed for the final image and package them into one dir
WORKDIR /deps

# Dependencies were found with the ldd command and then copied here to the /deps directory.
# For example: ldd /go/src/github.com/kyma-incubator/knative-kafka/components/controller/build/kafka-channel-controller
# Other dependencies that aren't found using the ldd command. These could be transitive dependencies.
# These were found at runtime generally, when the image reported runtime errors.
RUN  cp /usr/lib/x86_64-linux-gnu/librdkafka.so.1 --parents . \
  && cp /usr/lib/x86_64-linux-gnu/libsasl2.so.2 --parents . \
  && cp /usr/lib/x86_64-linux-gnu/libssl.so.1.0.0 --parents . \
  && cp /usr/lib/x86_64-linux-gnu/libcrypto.so.1.0.0 --parents . \
  && cp /lib/x86_64-linux-gnu/libpthread.so.0 --parents . \
  && cp /lib/x86_64-linux-gnu/libc.so.6 --parents . \
  && cp /lib/x86_64-linux-gnu/libm.so.6 --parents . \
  && cp /lib/x86_64-linux-gnu/libz.so.1 --parents . \
  && cp /lib/x86_64-linux-gnu/libdl.so.2 --parents . \
  && cp /lib/x86_64-linux-gnu/librt.so.1 --parents . \
  && cp /lib/x86_64-linux-gnu/libresolv.so.2 --parents . \
  && cp /lib/x86_64-linux-gnu/libnss_dns.so.2 --parents . \
  && cp /lib/x86_64-linux-gnu/libresolv.so.2 --parents . \
  && cp /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 --parents . \
  && cp /lib64/ld-linux-x86-64.so.2 --parents .

# Create Docker Container From Google's distroless base
FROM gcr.io/distroless/base

# Manage malloc and os thread count that can execute go code.
ENV MALLOC_ARENA_MAX=1 GOMAXPROCS=1

# Copy over the dependencies
COPY --from=builder /deps/ /
COPY --from=builder /tmp-certs/microsoft.crt /etc/ssl/certs/microsoft.crt

# Provides The SSL Cert For The Base Image To Properly Add It To The Cert Store
ENV SSL_CERT_FILE /etc/ssl/certs/microsoft.crt
