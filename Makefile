.PHONY: all test build clean test-live fmt vet check docker-build smoke-headless

BINARY := bin/code-agent-go
IMAGE := code-agent-go:latest

all: test build

test:
	go test -v -count=1 ./...

# 提交前自检：格式 + 静态检查 + 全量单测（CI 应直接复用这个目标）。
check: fmt vet test

fmt:
	gofmt -l -w cmd internal

vet:
	go vet ./...

build:
	go build -o $(BINARY) ./cmd

# 打包运行镜像：distroless 基础镜像 + 静态链接二进制，无 shell。
docker-build:
	docker build -t $(IMAGE) .

# 实时大模型连接测试：从 .env 读取凭证（存在时），运行集成测试。
# 复制 .env.example 为 .env 并填入真实值后即可使用。
test-live:
	set -a; [ -f .env ] && . ./.env; set +a; \
	go test -v -count=1 -run TestOpenAIProvider_GenerateStream_Live ./internal/provider/

clean:
	rm -f $(BINARY)

# 无头模式端到端冒烟：不需要真实 API Key / 网络。
# 用 scripts/fake_openai.py 顶替模型，验证 -p、管道输入、--output json、
# 高危命令默认拒绝与 -y 放行、缺输入提示、SIGINT 退出码。
# 依赖 python3；端口冲突时可用 PORT=8124 make smoke-headless。
smoke-headless:
	@scripts/smoke_headless.sh
