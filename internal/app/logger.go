package app

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/logging/zaplog"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type zapBuilderFunc func(zap.Config, ...zap.Option) (*zap.Logger, error)

func initAppLogger(cfg *config.Config) (*zaplog.Adapter, *zap.Logger, zap.AtomicLevel) {
	return initAppLoggerWith(cfg, os.Stderr, func(zc zap.Config, opts ...zap.Option) (*zap.Logger, error) {
		return zc.Build(opts...)
	})
}

func initAppLoggerWith(cfg *config.Config, errOut io.Writer, build zapBuilderFunc) (*zaplog.Adapter, *zap.Logger, zap.AtomicLevel) {
	if errOut == nil {
		errOut = os.Stderr
	}
	if build == nil {
		build = func(zc zap.Config, opts ...zap.Option) (*zap.Logger, error) {
			return zc.Build(opts...)
		}
	}

	logLevel := "info"
	logEnv := "production"
	logFormat := ""
	if cfg != nil {
		if v := strings.ToLower(strings.TrimSpace(cfg.LogLevel)); v != "" {
			logLevel = v
		}
		if v := strings.ToLower(strings.TrimSpace(cfg.LogEnv)); v != "" {
			logEnv = v
		}
		logFormat = strings.ToLower(strings.TrimSpace(cfg.LogFormat))
	}

	var zapLevel zapcore.Level
	switch logLevel {
	case "debug":
		zapLevel = zap.DebugLevel
	case "warn":
		zapLevel = zap.WarnLevel
	case "error":
		zapLevel = zap.ErrorLevel
	default:
		zapLevel = zap.InfoLevel
	}
	atomicLevel := zap.NewAtomicLevelAt(zapLevel)

	if logFormat != "json" && logFormat != "console" {
		if logEnv == "development" {
			logFormat = "console"
		} else {
			logFormat = "json"
		}
	}

	var zapCfg zap.Config
	if logEnv == "development" {
		zapCfg = zap.NewDevelopmentConfig()
	} else {
		zapCfg = zap.NewProductionConfig()
		zapCfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
		zapCfg.Sampling = &zap.SamplingConfig{
			Initial:    100,
			Thereafter: 10,
		}
	}
	zapCfg.Encoding = logFormat
	zapCfg.Level = atomicLevel

	z, err := build(zapCfg, zap.AddStacktrace(zapcore.ErrorLevel))
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "logger initialization failed (%v); falling back to stderr logger\n", err)
		z = newFallbackStderrLogger(errOut, atomicLevel)
	}
	return zaplog.Wrap(z), z, atomicLevel
}

func newFallbackStderrLogger(errOut io.Writer, level zap.AtomicLevel) *zap.Logger {
	if errOut == nil {
		errOut = os.Stderr
	}
	encCfg := zap.NewProductionEncoderConfig()
	encCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(encCfg),
		zapcore.AddSync(errOut),
		level,
	)
	return zap.New(core, zap.AddStacktrace(zapcore.ErrorLevel))
}

func syncAppLogger(adapter *zaplog.Adapter, errOut io.Writer) error {
	if adapter == nil {
		return nil
	}
	if errOut == nil {
		errOut = os.Stderr
	}
	if err := adapter.Sync(); err != nil {
		_, _ = fmt.Fprintf(errOut, "logger sync failed: %v\n", err)
		return err
	}
	return nil
}

// initLogger is a test-compatibility helper delegating to initAppLogger.
func initLogger(logLevel string) (*zap.Logger, zap.AtomicLevel, error) {
	_, z, lvl := initAppLogger(&config.Config{
		LogLevel:  logLevel,
		LogEnv:    "development",
		LogFormat: "console",
	})
	return z, lvl, nil
}
