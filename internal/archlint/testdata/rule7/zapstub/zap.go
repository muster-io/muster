// Package zap is a stub of go.uber.org/zap, so that the fixture needs no network and no real dependency.
package zap

import "go.uber.org/zap/zapcore"

type Logger struct{}

func L() *Logger { return &Logger{} }

func (l *Logger) Info(msg string, fields ...zapcore.Field) {}
