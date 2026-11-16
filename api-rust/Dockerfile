# syntax=docker/dockerfile:1
# delivery-eta Rust 서비스. 저장소 루트를 빌드 컨텍스트로 쓴다.
#   docker build -f api-rust/Dockerfile -t delivery-eta-rust .
#   docker run --rm -p 8082:8080 delivery-eta-rust

FROM rust:1.100-bookworm AS build
ARG TARGETARCH

# ONNX Runtime 공유 라이브러리. 버전은 uv.lock 의 onnxruntime 을 따라가 Python 서빙과 맞춘다.
# ort 크레이트는 load-dynamic 이라 빌드 때는 필요 없고, 실행 때 ORT_LIB 경로에서 읽는다.
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
COPY api-rust/Cargo.toml api-rust/Cargo.lock ./
COPY api-rust/src ./src
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/src/target \
    cargo test --release --locked \
 && cargo build --release --locked \
 && cp target/release/delivery-eta-rust /out/delivery-eta-rust

FROM debian:bookworm-slim
COPY --from=build /out/lib/ /usr/local/lib/
COPY --from=build /out/delivery-eta-rust /usr/local/bin/delivery-eta-rust
WORKDIR /app
COPY artifacts/model_sklearn.onnx artifacts/model_sklearn.onnx

ENV ORT_LIB=/usr/local/lib/libonnxruntime.so \
    MODEL_PATH=/app/artifacts/model_sklearn.onnx \
    PORT=8080
USER nobody
EXPOSE 8080
CMD ["delivery-eta-rust"]
