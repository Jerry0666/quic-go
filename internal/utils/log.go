package utils

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// LogLevel of quic-go
type LogLevel uint8

const (
	// LogLevelNothing disables
	LogLevelNothing LogLevel = iota
	// LogLevelError enables err logs
	LogLevelError
	// LogLevelInfo enables info logs (e.g. packets)
	LogLevelInfo
	// LogLevelDebug enables debug logs (e.g. packet contents)
	LogLevelDebug
	// LogLevelTrace enables trace some procedure and function
	LogLevelTrace
)

const logEnv = "QUIC_GO_LOG_LEVEL"
const globalLogLevel = LogLevelDebug

// A Logger logs.
type Logger interface {
	SetLogLevel(LogLevel)
	SetLogTimeFormat(format string)
	WithPrefix(prefix string) Logger
	Debug() bool

	Errorf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Debugf(format string, args ...interface{})
}

// DefaultLogger is used by quic-go for logging.
var DefaultLogger Logger

type defaultLogger struct {
	prefix string

	logLevel   LogLevel
	timeFormat string
}

var _ Logger = &defaultLogger{}

// SetLogLevel sets the log level
func (l *defaultLogger) SetLogLevel(level LogLevel) {
	l.logLevel = level
}

// SetLogTimeFormat sets the format of the timestamp
// an empty string disables the logging of timestamps
func (l *defaultLogger) SetLogTimeFormat(format string) {
	log.SetFlags(0) // disable timestamp logging done by the log package
	l.timeFormat = format
}

// Debugf logs something
func (l *defaultLogger) Debugf(format string, args ...interface{}) {
	if l.logLevel == LogLevelDebug {
		l.logMessage(format, args...)
	}
}

// Debug log enter a func, first bucket contain the func structure
func DebugLogEnterfunc(format string, args ...interface{}) {
	if globalLogLevel == LogLevelDebug {
		fmt.Printf("\033[32m"+format+"\033[0m\n", args...)
	}
}

func DebugNormolLog(format string, args ...interface{}) {
	if globalLogLevel == LogLevelDebug {
		fmt.Printf("\033[32m"+format+"\033[0m\n", args...)
	}
}

func TemporaryLog(format string, args ...interface{}) {
	if globalLogLevel == LogLevelDebug {
		fmt.Printf("\033[95m"+format+"\033[0m\n", args...)
	}
}

func DebugLogErr(format string, args ...interface{}) {
	if globalLogLevel == LogLevelDebug {
		fmt.Printf("\033[31m"+format+"\033[0m\n", args...)
	}
}

// Infof logs something
func (l *defaultLogger) Infof(format string, args ...interface{}) {
	if l.logLevel >= LogLevelInfo {
		l.logMessage(format, args...)
	}
}

// Errorf logs something
func (l *defaultLogger) Errorf(format string, args ...interface{}) {
	if l.logLevel >= LogLevelError {
		l.logMessage(format, args...)
	}
}

func (l *defaultLogger) logMessage(format string, args ...interface{}) {
	var pre string

	if len(l.timeFormat) > 0 {
		fmt.Println("timeFormat != nil")
		pre = time.Now().Format(l.timeFormat) + " "
	}
	if len(l.prefix) > 0 {
		pre += "[" + l.prefix + "]" + " "
	}
	log.Printf(pre+format, args...)
}

func (l *defaultLogger) WithPrefix(prefix string) Logger {
	if len(l.prefix) > 0 {
		prefix = l.prefix + " " + prefix
	}
	return &defaultLogger{
		logLevel:   l.logLevel,
		timeFormat: l.timeFormat,
		prefix:     prefix,
	}
}

// Debug returns true if the log level is LogLevelDebug
func (l *defaultLogger) Debug() bool {
	return l.logLevel == LogLevelDebug
}

func init() {
	DefaultLogger = &defaultLogger{}
	DefaultLogger.SetLogLevel(readLoggingEnv())
}

func readLoggingEnv() LogLevel {
	fmt.Println("set log level to debug")
	switch strings.ToLower("debug") {
	case "":
		return LogLevelNothing
	case "debug":
		return LogLevelDebug
	case "info":
		return LogLevelInfo
	case "error":
		return LogLevelError
	default:
		fmt.Fprintln(os.Stderr, "invalid quic-go log level, see https://github.com/quic-go/quic-go/wiki/Logging")
		return LogLevelNothing
	}
}
