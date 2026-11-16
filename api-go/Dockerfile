# syntax=docker/dockerfile:1
# delivery-eta Go 서비스. 저장소 루트를 빌드 컨텍스트로 쓴다.
#   docker build -f api-go/Dockerfile -t delivery-eta-go .
#   docker run --rm -p 8081:8080 delivery-eta-go

FROM golang:1.27-bookworm AS build
ARG TARGETARCH

# ONNX Runtime 공유 라이브러리. 버전은 uv.lock 의 onnxruntime 을 따라가 Python 서빙과 맞춘다.
COPY uv.lock /tmp/uv.lock
RUN set -eu; \
    ORT_VERSION="$(awk '$0 == "name = \"onnxruntime\"" { getline; gsub(/^version = "|"$/, ""); print; exit }' /tmp/uv.lock)"; \
    echo "$ORT_VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$' || { echo "uv.lock 에서 onnxruntime 버전을 읽지 못했다: '$ORT_VERSION'" >&2; exit 1; }; \
    ARCH="${TARGETARCH:-$(dpkg --print-architecture)}"; \
    case "$ARCH" in \
      amd64) ORT_ARCH=x64 ;; \
      arm64) ORT_ARCH=aarch64 ;; \
      *) echo "지원하지 않는 TARGETARCH: $ARCH" >&2; exit 1 ;; \
    esac; \
    NAME="onnxruntime-linux-${ORT_ARCH}-${ORT_VERSION}"; \
    curl -fsSL "https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/${NAME}.tgz" | tar -xz -C /tmp; \
    mkdir -p /out/lib; \
    cp -P /tmp/${NAME}/lib/libonnxruntime.so* /out/lib/; \
    echo "ONNX Runtime ${ORT_VERSION} (${ORT_ARCH})"

WORKDIR /src
COPY api-go/go.mod api-go/go.sum ./
RUN go mod download
COPY api-go/*.go ./
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/delivery-eta-go .

FROM debian:bookworm-slim
COPY --from=build /out/lib/ /usr/local/lib/
COPY --from=build /out/delivery-eta-go /usr/local/bin/delivery-eta-go
WORKDIR /app
COPY artifacts/model_sklearn.onnx artifacts/model_sklearn.onnx

ENV ONNXRUNTIME_LIB=/usr/local/lib/libonnxruntime.so \
    MODEL_PATH=/app/artifacts/model_sklearn.onnx \
    PORT=8080
USER nobody
EXPOSE 8080
CMD ["delivery-eta-go"]
