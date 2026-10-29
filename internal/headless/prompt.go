package headless

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNoPrompt 表示无头运行没有拿到任何输入任务。
// 单独定义一个哨兵而不是返回泛化错误：调用方要据此打印"怎么用"而不是当作运行失败。
var ErrNoPrompt = errors.New("未提供任务输入")

// maxStdinPrompt 是从 stdin 读取任务时的上限。
// stdin 可能是被别的文件（甚至二进制）喂进来的，无上限读取会把整文件塞进上下文。
const maxStdinPrompt = 64 << 10

// ReadPrompt 决定无头模式下的任务文本，优先级：位置参数 > stdin。
//
// stdin 只有在确认"有内容且不是终端"时才读：终端上的 ReadAll 会一直等到 Ctrl-D，
// 用户看到的就是"程序卡住了"，而实际上它只是在等一个永远不会自己到来的 EOF。
func ReadPrompt(args []string, stdin io.Reader, stdinAvailable bool) (string, error) {
	if len(args) > 0 {
		if p := strings.TrimSpace(strings.Join(args, " ")); p != "" {
			return p, nil
		}
	}
	if stdinAvailable && stdin != nil {
		b, err := io.ReadAll(io.LimitReader(stdin, maxStdinPrompt))
		if err != nil {
			// 读取失败（例如 stdin 被关闭）依旧包装成 ErrNoPrompt：
			// 调用方据此打印"怎么传任务"，而不是抛出一个用户无从下手的 I/O 错误。
			return "", fmt.Errorf("%w: 读取 stdin 失败: %v", ErrNoPrompt, err)
		}
		if p := strings.TrimSpace(string(b)); p != "" {
			return p, nil
		}
	}
	return "", ErrNoPrompt
}
