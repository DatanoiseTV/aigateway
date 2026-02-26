package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	Logger *zap.Logger
	Sugar  *zap.SugaredLogger
)

func Init(debug bool) error {
	var cfg zap.Config
	if debug {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		cfg = zap.NewProductionConfig()
		cfg.Encoding = "json"
	}

	logger, err := cfg.Build()
	if err != nil {
		return err
	}

	Logger = logger
	Sugar = logger.Sugar()
	return nil
}

func InitSilent() {
	Logger = zap.NewNop()
	Sugar = Logger.Sugar()
}

func Sync() {
	if Logger != nil {
		Logger.Sync()
	}
}
