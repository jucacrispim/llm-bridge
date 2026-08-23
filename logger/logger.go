package logger

import (
	"errors"
	"io"
	"log"
	"os"
	"strings"
)

type Level int

const (
	LevelTrace Level = iota
	LevelDebug
	LevelInfo
	LevelWarning
	LevelError
)

var (
	traceLogger   = log.New(os.Stdout, "[TRACE] ", 0)
	debugLogger   = log.New(os.Stdout, "[DEBUG] ", 0)
	infoLogger    = log.New(os.Stdout, "[INFO] ", 0)
	warningLogger = log.New(os.Stderr, "[WARNING] ", 0)
	errorLogger   = log.New(os.Stderr, "[ERROR] ", 0)

	currentLogLevel Level = LevelInfo
)

func SetLogLevel(level Level) {
	currentLogLevel = level
}

func SetLogLevelStr(levelstr string) error {
	norm := strings.ToLower(levelstr)
	var lvl Level
	switch norm {
	case "trace":
		lvl = LevelTrace
	case "debug":
		lvl = LevelDebug
	case "info":
		lvl = LevelInfo
	case "warning":
		lvl = LevelWarning
	case "error":
		lvl = LevelError
	default:
		return errors.New("invalid log level")
	}
	SetLogLevel(lvl)
	return nil
}

func GetLogLevel() Level {
	return currentLogLevel
}

// SetOutput redirects all loggers (trace, debug, info, warning and error) to
// the given writer. Use io.MultiWriter to keep writing to multiple destinations.
func SetOutput(w io.Writer) {
	traceLogger.SetOutput(w)
	debugLogger.SetOutput(w)
	infoLogger.SetOutput(w)
	warningLogger.SetOutput(w)
	errorLogger.SetOutput(w)
}

func Tracef(format string, v ...interface{}) {
	if currentLogLevel <= LevelTrace {
		traceLogger.Printf(format, v...)
	}
}

func Debugf(format string, v ...interface{}) {
	if currentLogLevel <= LevelDebug {
		debugLogger.Printf(format, v...)
	}
}

func Infof(format string, v ...interface{}) {
	if currentLogLevel <= LevelInfo {
		infoLogger.Printf(format, v...)
	}
}

func Warningf(format string, v ...interface{}) {
	if currentLogLevel <= LevelWarning {
		warningLogger.Printf(format, v...)
	}
}

func Errorf(format string, v ...interface{}) {
	if currentLogLevel <= LevelError {
		errorLogger.Printf(format, v...)
	}
}
