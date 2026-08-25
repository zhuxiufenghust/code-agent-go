package log

import "go.uber.org/zap"

var logger *zap.Logger

func NewLogger(config *zap.Config) {
	var err error
	logger, err = config.Build()
	if err != nil {
		panic(err)
	}
	// log 包对 zap 做了一层封装，跳过 1 帧以定位到真正的调用方代码文件与行号。
	logger = logger.WithOptions(zap.AddCallerSkip(1))
}

func Info(msg string, fields ...zap.Field) {
	logger.Info(msg, fields...)
}

func Error(msg string, fields ...zap.Field) {
	logger.Error(msg, fields...)
}
func Debug(msg string, fields ...zap.Field) {
	logger.Debug(msg, fields...)
}

func Warn(msg string, fields ...zap.Field) {
	logger.Warn(msg, fields...)
}
