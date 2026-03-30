# Base runtime image with heavy deps (cached separately)
FROM debian:bookworm-slim AS runtime-base

RUN apt-get update && apt-get install -y --no-install-recommends \
    chromium \
    fonts-liberation \
    fonts-noto-color-emoji \
    fonts-noto-cjk \
    fonts-noto-cjk-extra \
    fonts-noto-core \
    fonts-dejavu-core \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN useradd -m -u 1000 appuser


# Build MuPDF 1.24.x from source (Debian bookworm only has 1.21.1, go-fitz v1.24.15 needs 1.24.x)
FROM debian:bookworm-slim AS mupdf-builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential wget ca-certificates pkg-config \
    && wget -q https://mupdf.com/downloads/archive/mupdf-1.24.9-source.tar.gz -O /tmp/mupdf.tar.gz \
    || wget -q https://github.com/ArtifexSoftware/mupdf/releases/download/1.24.9/mupdf-1.24.9-source.tar.gz -O /tmp/mupdf.tar.gz \
    && tar xzf /tmp/mupdf.tar.gz -C /tmp 
    
RUN cd /tmp/mupdf-1.24.9-source \
    && make USE_SYSTEM_LIBS=no HAVE_X11=no HAVE_GLUT=no shared=yes libs -j$(nproc) \
    && cp build/shared-release/libmupdf.so /usr/lib/libmupdf.so \
    && ldconfig \
    && rm -rf /tmp/mupdf* /var/lib/apt/lists/*


# Build stage
FROM golang:1.25-bookworm AS builder

# No CGO build dependencies needed - go-fitz v1.24+ uses purego/ffi

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with cache
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 go build -o /app/auto_print ./cmd/main.go


# Runtime stage
FROM runtime-base

WORKDIR /app

# Install runtime deps for libmupdf.so and copy it
RUN apt-get update && apt-get install -y --no-install-recommends \
    libjpeg62-turbo libopenjp2-7 libharfbuzz0b libfreetype6 libgumbo1 zlib1g \
    && rm -rf /var/lib/apt/lists/*
COPY --from=mupdf-builder /usr/lib/libmupdf.so /usr/lib/libmupdf.so
RUN ldconfig

# Copy binary from builder
COPY --from=builder /app/auto_print /app/auto_print

# Create output and logs directories
RUN mkdir -p output logs && chown -R appuser:appuser /app

# Switch to non-root user
USER appuser

# Set Chromium path for go-rod
ENV ROD_BROWSER=/usr/bin/chromium

# Entrypoint
CMD ["/app/auto_print"]
