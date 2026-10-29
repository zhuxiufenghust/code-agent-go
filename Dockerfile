# syntax=docker/dockerfile:1

# ---------- 构建阶段 ----------
# 用固定小版本 tag 而不是 latest：镜像内容可复现，CI 不会因为上游滚动升级而随机崩。
FROM golang:1.26-alpine AS builder

WORKDIR /src

# 依赖先行：单独 COPY go.mod/go.sum 让 docker 缓存命中率最大化，
# 改动业务代码时不必重新下载全部模块。
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

# CGO_ENABLED=0 + 静态链接：scratch/distroless 里没有 libc，
# 动态链接的二进制会以 "not found" 这种完全看不懂的方式启动失败。
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/code-agent-go ./cmd

# ---------- 运行阶段 ----------
# distroless 保持"容器里只有这一个进程"的最小攻击面；没有 shell 也意味着
# 即便有人拿到了容器内的执行能力，也没有 ls/sh 可用。
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

WORKDIR /app

COPY --from=builder /out/code-agent-go /app/code-agent-go
COPY conf/config.json /app/conf/config.json

# 运行时状态（会话库、长期记忆、信任态、日志）一律落到 HOME 下：
# distroless 的 / 只读，写根目录必然失败。
ENV HOME=/home/nonroot
ENV XDG_CONFIG_HOME=/home/nonroot/.config

# 两种用法都支持，取决于有没有终端：
#   - 交互（有 TTY）：docker run -it --rm <image>
#   - 无头（无 TTY）：docker run --rm <image> -p "任务描述"
# 无 TTY 时入口会自动走无头路径（见 internal/headless），不再因为拿不到终端而直接失败。
ENTRYPOINT ["/app/code-agent-go"]
CMD ["--config", "/app/conf/config.json"]
