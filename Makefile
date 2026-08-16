.PHONY: all test build clean test-live

BINARY := bin/code-agent-go

all: test build

test:
	go test -v -count=1 ./...

# 实时大模型连接测试：从 .env 读取凭证（存在时），运行集成测试。
# 复制 .env.example 为 .env 并填入真实值后即可使用。
test-live:
	set -a; [ -f .env ] && . ./.env; set +a; \
	go test -v -count=1 -run TestOpenAIProvider_GenerateStream_Live ./internal/provider/

build:
	go build -o $(BINARY) ./cmd

clean:
	rm -f $(BINARY)
