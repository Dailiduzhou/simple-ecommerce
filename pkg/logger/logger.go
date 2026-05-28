package logger

import (
	"os"

	kratoszap "github.com/go-kratos/kratos/contrib/log/zap/v2"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/tracing"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

func NewJSONLogger() log.Logger {
	// 1. 配置 Lumberjack 进行日志轮转
	hook := &lumberjack.Logger{
		Filename:   "./logs/app.log", // 重点：Promtail 将读取这个目录
		MaxSize:    100,              // 最大尺寸 (MB)
		MaxBackups: 5,                // 最大备份数
		MaxAge:     30,               // 最大保留天数
		Compress:   true,             // 是否压缩过期日志
	}

	// 2. 配置 Zap 的 JSON 编码器
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder // 使用人类可读的时间格式

	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encoderConfig),
		zapcore.NewMultiWriteSyncer(
			zapcore.AddSync(os.Stdout),
			zapcore.AddSync(hook),
		),
		zap.InfoLevel,
	)

	zlogger := zap.New(core)

	// 3. 包装为 Kratos 的 Logger
	logger := kratoszap.NewLogger(zlogger)

	// 4. 注入全局公共字段（TraceID 是排查问题的核心）
	return log.With(logger,
		"ts", log.DefaultTimestamp,
		"caller", log.DefaultCaller,
		"service.name", "simple-seckill",
		"trace.id", tracing.TraceID(), // 自动从 context 提取 traceID
		"span.id", tracing.SpanID(),
	)
}
