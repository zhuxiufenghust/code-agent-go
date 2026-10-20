#!/usr/bin/env python3
"""本地假 OpenAI 兼容服务：只为无头模式冒烟服务，不接真实模型。

能力刻意做到"刚刚够用"：
  - POST /v1/chat/completions，支持流式（SSE）与非流式两种形态；
  - 第一轮（请求里带 tools 且历史里还没有 tool 消息）固定只发一次 bash_tool 调用，
    命令从 $FAKE_OPENAI_COMMAND_FILE 读取（脚本可在不重启服务的情况下换命令）；
  - 第二轮（已回灌工具结果）输出固定文本，便于断言"来源唯一的答案"。

之所以要它：无头模式的验证不该依赖真实 API Key 和网络，
否则"验证"本身就有配额、限流、价格波动三个不确定性来源。

环境变量：
  FAKE_OPENAI_PORT          监听端口（默认 8123）
  FAKE_OPENAI_COMMAND_FILE  每轮读取的工具命令文件
  FAKE_OPENAI_DELAY_FILE    每轮读取的响应延迟（秒），用于信号/中断场景
  FAKE_OPENAI_MODEL         响应里的 model 字段（默认 smoke-model）
"""

import json
import os
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("FAKE_OPENAI_PORT", "8123"))
MODEL = os.environ.get("FAKE_OPENAI_MODEL", "smoke-model")
COMMAND_FILE = os.environ.get("FAKE_OPENAI_COMMAND_FILE", "")
DELAY_FILE = os.environ.get("FAKE_OPENAI_DELAY_FILE", "")
DEFAULT_COMMAND = "echo headless-smoke"
FINAL_TEXT = ["无头模式", "跑通了。"]


def read_file(path, default=""):
    try:
        with open(path, "r", encoding="utf-8") as f:
            return f.read().strip()
    except OSError:
        return default


def next_command():
    return read_file(COMMAND_FILE, DEFAULT_COMMAND) or DEFAULT_COMMAND


def next_delay():
    try:
        return max(0.0, float(read_file(DELAY_FILE, "0") or 0))
    except ValueError:
        return 0.0


def delta(delta_obj, finish=None):
    return {
        "id": "chatcmpl-smoke",
        "object": "chat.completion.chunk",
        "created": 1,
        "model": MODEL,
        "choices": [{"index": 0, "delta": delta_obj, "finish_reason": finish}],
    }


def usage_chunk():
    return {
        "id": "chatcmpl-smoke",
        "object": "chat.completion.chunk",
        "created": 1,
        "model": MODEL,
        "choices": [],
        "usage": {"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17},
    }


class Handler(BaseHTTPRequestHandler):
    # HTTP/1.1 + Connection: close：流式响应没有 Content-Length，
    # 不显式声明关闭会让客户端一直等下一个字节。
    protocol_version = "HTTP/1.1"

    def log_message(self, *args):
        pass  # 冒烟脚本要看自己的断言输出，不想被 HTTP 访问日志淹没

    def do_POST(self):
        time.sleep(next_delay())
        length = int(self.headers.get("content-length") or 0)
        body = json.loads(self.rfile.read(length) or b"{}")
        msgs = body.get("messages") or []
        tools = body.get("tools") or []
        has_tool_result = any(m.get("role") == "tool" for m in msgs)

        if tools and not has_tool_result:
            chunks = self.tool_call_chunks(next_command())
        else:
            chunks = self.text_chunks()
        if body.get("stream", True):
            self.stream(chunks)
        else:
            self.non_stream(chunks)

    @staticmethod
    def tool_call_chunks(command):
        head = {
            "index": 0,
            "id": "call_smoke_1",
            "type": "function",
            "function": {"name": "bash_tool", "arguments": ""},
        }
        return [
            delta({"role": "assistant", "content": None}),
            delta({"tool_calls": [head]}),
            delta({"tool_calls": [{"index": 0, "function": {"arguments": json.dumps({"command": command})}}]}),
            delta({}, finish="tool_calls"),
            usage_chunk(),
        ]

    @staticmethod
    def text_chunks():
        chunks = [delta({"content": part}) for part in FINAL_TEXT]
        chunks.append(delta({}, finish="stop"))
        chunks.append(usage_chunk())
        return chunks

    def stream(self, chunks):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Connection", "close")
        self.end_headers()
        for c in chunks:
            self.wfile.write(("data: " + json.dumps(c) + "\n\n").encode())
        self.wfile.write(b"data: [DONE]\n\n")
        self.wfile.flush()
        self.close_connection = True

    def non_stream(self, chunks):
        # 非流式也要可用：某些 Provider 路径会先试一次非流式（或健康检查），
        # 返回 404 会让调用方以为是配置问题而排查错方向。
        content = "".join(
            c["choices"][0]["delta"].get("content") or "" for c in chunks if c.get("choices")
        )
        body = {
            "id": "chatcmpl-smoke",
            "object": "chat.completion",
            "created": 1,
            "model": MODEL,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17},
        }
        raw = json.dumps(body).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(raw)
        self.wfile.flush()
        self.close_connection = True


def main():
    server = ThreadingHTTPServer(("127.0.0.1", PORT), Handler)
    print("fake-openai listening on http://127.0.0.1:%d" % PORT, flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
