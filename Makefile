GO ?= go
CARGO ?= cargo
DIST := dist
AARCH64_TARGET := aarch64-unknown-linux-gnu

.PHONY: all build build-arm64 build-local rust-arm64 rust-local test fmt clean

all: build-local

## 在 x86 上交叉编译（需 gcc-aarch64-linux-gnu + libc6-dev-arm64-cross）
## 产物：Go 静态链接 / Rust 动态链接系统 glibc
build-arm64: $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" -o $(DIST)/chenxi ./cmd/chenxi
	$(MAKE) rust-arm64

## 在 aarch64 主机（Armbian 板子 / arm64 runner）上原生编译，无需交叉链接器
## 注意：不要在 x86 上跑这条，Rust 会产出宿主架构二进制。
build-local: $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" -o $(DIST)/chenxi ./cmd/chenxi
	cd hostd && $(CARGO) build --release
	cp hostd/target/release/chenxi-hostd $(DIST)/chenxi-hostd

rust-arm64:
	cd hostd && $(CARGO) build --release --target $(AARCH64_TARGET)
	cp hostd/target/$(AARCH64_TARGET)/release/chenxi-hostd $(DIST)/chenxi-hostd

## 纯 Go 部分也能在 x86 本机跑起来（hostd 需另起）
build-local-dev: $(DIST)
	CGO_ENABLED=0 $(GO) build -trimpath -o $(DIST)/chenxi ./cmd/chenxi

fmt:
	$(GO) fmt ./...
	cd hostd && $(CARGO) fmt

test:
	$(GO) vet ./...
	$(GO) test ./...
	cd hostd && $(CARGO) check

$(DIST):
	mkdir -p $(DIST)

clean:
	rm -rf $(DIST) hostd/target
