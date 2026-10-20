#!/usr/bin/env bash
# 无头模式端到端冒烟：不依赖真实 LLM Key、不依赖网络、不污染用户目录。
#
# 用一个本地假 OpenAI 兼容服务（scripts/fake_openai.py）替身模型，
# 依次验证：位置参数、管道输入、--output json、高危命令默认拒绝、-y 放行、SIGINT 退出码。
#
# 用法：make smoke-headless（或 scripts/smoke_headless.sh）

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

PORT="${PORT:-8123}"
PYTHON="${PYTHON:-python3}"
BIN="bin/code-agent-go"
CONFIG="scripts/headless_smoke_config.json"
SERVER="scripts/fake_openai.py"

GREEN=$'\033[32m'; RED=$'\033[31m'; RESET=$'\033[0m'
FAILURES=0

ok()   { printf '%sPASS%s %s\n' "$GREEN" "$RESET" "$1"; }
bad()  { printf '%sFAIL%s %s\n' "$RED" "$RESET" "$1"; FAILURES=$((FAILURES + 1)); }
assert_eq() { # 说明 实际 期望
	if [ "$2" = "$3" ]; then ok "$1"; else bad "$1（期望: $3，实际: $2）"; fi
}
assert_contains() { # 说明 文件 关键字
	if grep -q -- "$3" "$2"; then ok "$1"; else bad "$1（未找到关键字: $3）"; fi
}

cleanup() {
	[ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null
	[ -n "${WORK:-}" ] && rm -rf "$WORK"
}
trap cleanup EXIT

command -v "$PYTHON" >/dev/null 2>&1 || { echo "需要 python3 来启动假 LLM 服务"; exit 1; }

"$PYTHON" - "$PORT" <<'PY' || { echo "端口 $1 已被占用，换一个：PORT=8124 make smoke-headless"; exit 1; }
import socket, sys
s = socket.socket()
try:
    s.connect(("127.0.0.1", int(sys.argv[1])))
    sys.exit(1)
except OSError:
    sys.exit(0)
finally:
    s.close()
PY

WORK="$(mktemp -d)"
HOME_DIR="$WORK/home"
mkdir -p "$HOME_DIR"
CMD_FILE="$WORK/tool_command"
DELAY_FILE="$WORK/delay"

echo "构建二进制..."
go build -o "$BIN" ./cmd || { echo "构建失败"; exit 1; }

echo "启动假 LLM 服务 (127.0.0.1:$PORT)..."
FAKE_OPENAI_PORT="$PORT" FAKE_OPENAI_COMMAND_FILE="$CMD_FILE" FAKE_OPENAI_DELAY_FILE="$DELAY_FILE" \
	"$PYTHON" "$SERVER" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!

"$PYTHON" - "$PORT" <<'PY' || { echo "假服务启动失败，日志："; cat "$WORK/server.log"; exit 1; }
import socket, sys, time
port = int(sys.argv[1])
for _ in range(60):
    try:
        socket.create_connection(("127.0.0.1", port), 0.2).close()
        sys.exit(0)
    except OSError:
        time.sleep(0.1)
sys.exit(1)
PY

# HOME 隔离：会话库 / 信任态都落在临时目录，跑完连同 printConfig 的产物一起丢弃。
export HOME="$HOME_DIR"
export TEST_OPENAI_API_KEY="smoke-key"
export TEST_OPENAI_BASE_URL="http://127.0.0.1:$PORT/v1"
echo "0" >"$DELAY_FILE"
echo "echo headless-smoke" >"$CMD_FILE"

run_agent() { timeout 90 "$BIN" -c "$CONFIG" -e "$@"; }

echo
echo "== 1) 位置参数无头运行：stdout 只应有助手正文 =="
run_agent -p "你好" >"$WORK/s1.out" 2>"$WORK/s1.err"
RC=$?
assert_eq "退出码为 0" "$RC" "0"
assert_eq "stdout 只有答案" "$(cat "$WORK/s1.out")" "无头模式跑通了。"
assert_contains "诊断信息走 stderr（工具调用可见）" "$WORK/s1.err" "调用工具 bash_tool"

echo
echo "== 2) 管道输入（不带 -p 也应自动降级）=="
echo "来自 stdin 的任务" | run_agent >"$WORK/s2.out" 2>"$WORK/s2.err"
RC=$?
assert_eq "退出码为 0" "$RC" "0"
assert_eq "答案来自管道任务" "$(cat "$WORK/s2.out")" "无头模式跑通了。"

echo
echo "== 3) --output json 可被脚本解析 =="
run_agent -p "hi" --output json >"$WORK/s3.json" 2>/dev/null
RC=$?
assert_eq "退出码为 0" "$RC" "0"
LAST_TYPE="$("$PYTHON" - "$WORK/s3.json" <<'PY'
import json, sys
lines = [l for l in open(sys.argv[1], encoding="utf-8").read().splitlines() if l.strip()]
print(json.loads(lines[-1])["type"] if lines else "empty")
PY
)"
assert_eq "末行为 result 事件" "$LAST_TYPE" "result"
assert_contains "中间含结构化工具事件" "$WORK/s3.json" '"type":"tool_start"'

echo
echo "== 4) 高危命令：无交互通道时默认拒绝，且不能真的执行 =="
TARGET="$WORK/danger-target"
mkdir -p "$TARGET"
echo "rm -rf $TARGET" >"$CMD_FILE"
run_agent -p "清理临时目录" >"$WORK/s4.out" 2>"$WORK/s4.err"
RC=$?
assert_eq "工具失败不视为运行失败，退出码仍为 0" "$RC" "0"
if [ -d "$TARGET" ]; then ok "目标目录仍在（命令没被执行）"; else bad "目标目录被删了（拦截失效）"; fi
assert_contains "摘要记录拒绝次数" "$WORK/s4.err" "高危命令被拒=1"

echo
echo "== 5) 显式 -y 放行后同一条命令应当真的执行 =="
TARGET2="$WORK/throwaway-target"
mkdir -p "$TARGET2"
echo "rm -rf $TARGET2" >"$CMD_FILE"
run_agent -y -p "清理一次性目录" >"$WORK/s5.out" 2>"$WORK/s5.err"
RC=$?
assert_eq "退出码为 0" "$RC" "0"
if [ -d "$TARGET2" ]; then bad "目录仍在（-y 未生效）"; else ok "目录已被删除（-y 生效）"; fi
assert_contains "日志记录信任态放行" "$WORK/s5.err" "trust_level\":\"trusted"

echo
echo "== 6) 缺少任务输入应当给出提示并退出 1 =="
run_agent -p </dev/null >"$WORK/s6.out" 2>"$WORK/s6.err"
RC=$?
assert_eq "退出码为 1" "$RC" "1"
assert_contains "打印使用提示" "$WORK/s6.err" "无头模式缺少任务输入"

echo
echo "== 7) 运行中收到终止信号应当返回 130 =="
# 用 SIGTERM 而非 SIGINT：后台作业默认继承"忽略 SIGINT"，
# 用 SIGINT 测出来的 130 会随 shell 是否为交互模式而变，测不准。
echo "5" >"$DELAY_FILE"
# 这里不能用 run_agent（它外面包了一层 timeout）：
# timeout 自己被 TERM 打死后会把退出码留成 143，掩盖我们要验的 130。
"$BIN" -c "$CONFIG" -e -p "慢任务" >"$WORK/s7.out" 2>"$WORK/s7.err" &
AGENT_PID=$!
sleep 2
kill -TERM "$AGENT_PID" 2>/dev/null
wait "$AGENT_PID" 2>/dev/null
RC=$?
assert_eq "退出码为 130" "$RC" "130"
echo "0" >"$DELAY_FILE"

echo
if [ "$FAILURES" -eq 0 ]; then
	printf '%s全部通过%s：无头模式 7 组场景 OK\n' "$GREEN" "$RESET"
	exit 0
fi
printf '%s失败 %d 项%s（保留临时目录：%s）\n' "$RED" "$FAILURES" "$RESET" "$WORK"
trap - EXIT
exit 1
