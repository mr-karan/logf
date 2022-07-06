package logf

import (
	"fmt"
	"io"
	stdlog "log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	tsKey           = "timestamp="
	scopeKey        = "sc"
	defaultTSFormat = "2006-01-02T15:04:05.999Z07:00"
)

var (
	hex     = "0123456789abcdef"
	bufPool byteBufferPool
)

// Logger is the interface for all log operations
// related to emitting logs.
type Logger struct {
	out                  io.Writer // Output destination.
	level                Level     // Verbosity of logs.
	tsFormat             string    // Timestamp format.
	enableColor          bool      // Colored output.
	enableCaller         bool      // Print caller information.
	callerSkipFrameCount int       // Number of frames to skip when detecting caller.
	scope                string    // Scope is a namespace which is included in every log under the `scopeKey`.
}

// Fields is a map of arbitrary KV pairs
// which will be used in logfmt representation of the log.
type Fields map[string]any

// Severity level of the log.
type Level int

const (
	DebugLevel Level = iota // 0
	InfoLevel               // 1
	WarnLevel               // 2
	ErrorLevel              // 3
	FatalLevel              // 4
)

// ANSI escape codes for coloring text in console.
const (
	reset  = "\033[0m"
	purple = "\033[35m"
	red    = "\033[31m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
)

// Map colors with log level.
var colorLvlMap = [...]string{
	DebugLevel: purple,
	InfoLevel:  cyan,
	WarnLevel:  yellow,
	ErrorLevel: red,
	FatalLevel: red,
}

// New instantiates a logger object.
// It writes to `stderr` as the default and it's non configurable.
func New(out io.Writer) Logger {
	// Initialise logger with sane defaults.
	if out == nil {
		out = os.Stderr
	}

	return Logger{
		out:                  newSyncWriter(out),
		level:                InfoLevel,
		tsFormat:             defaultTSFormat,
		enableColor:          false,
		enableCaller:         false,
		callerSkipFrameCount: 0,
		scope:                "general",
	}
}

// syncWriter is a wrapper around io.Writer that
// synchronizes writes using a mutex.
type syncWriter struct {
	sync.Mutex
	w io.Writer
}

// Write synchronously to the underlying io.Writer.
func (w *syncWriter) Write(p []byte) (int, error) {
	w.Lock()
	n, err := w.w.Write(p)
	w.Unlock()
	return n, err
}

// newSyncWriter wraps an io.Writer with syncWriter. It can
// be used as an io.Writer as syncWriter satisfies the io.Writer interface.
func newSyncWriter(in io.Writer) *syncWriter {
	if in == nil {
		return &syncWriter{w: os.Stderr}
	}

	return &syncWriter{w: in}
}

// String representation of the log severity.
func (l Level) String() string {
	switch l {
	case DebugLevel:
		return "debug"
	case InfoLevel:
		return "info"
	case WarnLevel:
		return "warn"
	case ErrorLevel:
		return "error"
	case FatalLevel:
		return "fatal"
	default:
		return "invalid lvl"
	}
}

// SetLevel sets the verbosity for logger.
// Verbosity can be dynamically changed by the caller.
func (l Logger) SetLevel(lvl Level) Logger {
	l.level = lvl
	return l
}

// SetWriter sets the output writer for the logger
func (l Logger) SetWriter(w io.Writer) Logger {
	l.out = &syncWriter{w: w}
	return l
}

// SetTimestampFormat sets the timestamp format for the `timestamp` key.
func (l Logger) SetTimestampFormat(f string) Logger {
	l.tsFormat = f
	return l
}

// SetColorOutput enables/disables colored output.
func (l Logger) SetColorOutput(color bool) Logger {
	l.enableColor = color
	return l
}

// SetCallerFrame enables/disables the caller source in the log line.
func (l Logger) SetCallerFrame(caller bool, depth int) Logger {
	l.enableCaller = caller
	l.callerSkipFrameCount = depth
	return l
}

// SetScope adds the namespace in the log line.
func (l *Logger) SetScope(scope string) {
	l.scope = scope
}

// Debug emits a debug log line.
func (l Logger) Debug(msg string) {
	l.handleLog(msg, DebugLevel, nil)
}

// Info emits a info log line.
func (l Logger) Info(msg string) {
	l.handleLog(msg, InfoLevel, nil)
}

// Warn emits a warning log line.
func (l Logger) Warn(msg string) {
	l.handleLog(msg, WarnLevel, nil)
}

// Error emits an error log line.
func (l Logger) Error(msg string) {
	l.handleLog(msg, ErrorLevel, nil)
}

// Fatal emits a fatal level log line.
// It aborts the current program with an exit code of 1.
func (l Logger) Fatal(msg string) {
	l.handleLog(msg, FatalLevel, nil)
	os.Exit(1)
}

// WithFields returns a new entry with `fields` set.
func (l Logger) WithFields(fields Fields) FieldLogger {
	return FieldLogger{
		fields: fields,
		logger: l,
	}
}

// WithError returns a Logger with the "error" key set to `err`.
func (l Logger) WithError(err error) FieldLogger {
	if err == nil {
		return FieldLogger{logger: l}
	}

	return l.WithFields(Fields{
		"error": err.Error(),
	})
}

// handleLog emits the log after filtering log level
// and applying formatting of the fields.
func (l Logger) handleLog(msg string, lvl Level, fields Fields) {
	// Discard the log if the verbosity is higher.
	// For eg, if the lvl is `3` (error), but the incoming message is `0` (debug), skip it.
	if lvl < l.level {
		return
	}

	// Get a buffer from the pool.
	buf := bufPool.Get()

	// Write fixed keys to the buffer before writing user provided ones.
	writeTimeToBuf(buf, l.tsFormat, lvl, l.enableColor)
	writeToBuf(buf, "level", lvl, lvl, l.enableColor, true)
	writeStringToBuf(buf, "message", msg, lvl, l.enableColor, true)
	writeStringToBuf(buf, scopeKey, l.scope, lvl, l.enableColor, true)

	if l.enableCaller {
		writeToBuf(buf, "caller", caller(l.callerSkipFrameCount), lvl, l.enableColor, true)
	}

	// Format the line as logfmt.
	var count int // count is find out if this is the last key in while itering fields.
	for k, v := range fields {
		space := false
		if count != len(fields)-1 {
			space = true
		}
		writeToBuf(buf, k, v, lvl, l.enableColor, space)
		count++
	}
	buf.AppendString("\n")

	_, err := l.out.Write(buf.Bytes())
	if err != nil {
		// Should ideally never happen.
		stdlog.Printf("error logging: %v", err)
	}

	buf.Reset()

	// Put the writer back in the pool.
	bufPool.Put(buf)
}

// writeTimeToBuf writes timestamp key + timestamp into buffer.
func writeTimeToBuf(buf *byteBuffer, format string, lvl Level, color bool) {
	if color {
		buf.AppendString(getColoredKey(tsKey, lvl))
	} else {
		buf.AppendString(tsKey)
	}

	buf.AppendTime(time.Now(), format)
	buf.AppendByte(' ')
}

// writeStringToBuf takes key, value and additional options to write to the buffer in logfmt.
func writeStringToBuf(buf *byteBuffer, key string, val string, lvl Level, color, space bool) {
	if color {
		escapeAndWriteString(buf, getColoredKey(key, lvl))
	} else {
		escapeAndWriteString(buf, key)
	}
	buf.AppendByte('=')
	escapeAndWriteString(buf, val)
	if space {
		buf.AppendByte(' ')
	}
}

// writeToBuf takes key, value and additional options to write to the buffer in logfmt.
func writeToBuf(buf *byteBuffer, key string, val any, lvl Level, color, space bool) {
	if color {
		escapeAndWriteString(buf, getColoredKey(key, lvl))
	} else {
		escapeAndWriteString(buf, key)
	}
	buf.AppendByte('=')

	switch v := val.(type) {
	case string:
		escapeAndWriteString(buf, v)
	case int:
		buf.AppendInt(int64(v))
	case int16:
		buf.AppendInt(int64(v))
	case int32:
		buf.AppendInt(int64(v))
	case int64:
		buf.AppendInt(int64(v))
	case float32:
		buf.AppendFloat(float64(v), 32)
	case float64:
		buf.AppendFloat(float64(v), 64)
	case fmt.Stringer:
		escapeAndWriteString(buf, v.String())
	default:
		escapeAndWriteString(buf, fmt.Sprintf("%v", val))
	}

	if space {
		buf.AppendByte(' ')
	}
}

// escapeAndWriteString escapes the string if any unwanted chars are there.
func escapeAndWriteString(buf *byteBuffer, s string) {
	idx := strings.IndexFunc(s, checkEscapingRune)
	if idx != -1 {
		writeQuotedString(buf, s)
		return
	}
	buf.AppendString(s)
}

// getColoredKey returns a color formatter key based on the log level.
func getColoredKey(k string, lvl Level) string {
	return colorLvlMap[lvl] + k + reset
}

// caller returns the file:line of the caller.
func caller(depth int) string {
	_, file, line, ok := runtime.Caller(depth)
	if !ok {
		file = "???"
		line = 0
	}
	return file + ":" + strconv.Itoa(line)
}

// checkEscapingRune returns true if the rune is to be escaped.
func checkEscapingRune(r rune) bool {
	return r == '=' || r == ' ' || r == '"' || r == utf8.RuneError
}

// writeQuotedString quotes a string before writing to the buffer.
// Taken from: https://github.com/go-logfmt/logfmt/blob/99455b83edb21b32a1f1c0a32f5001b77487b721/jsonstring.go#L95
func writeQuotedString(buf *byteBuffer, s string) {
	buf.AppendByte('"')
	start := 0
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if 0x20 <= b && b != '\\' && b != '"' {
				i++
				continue
			}
			if start < i {
				buf.AppendString(s[start:i])
			}
			switch b {
			case '\\', '"':
				buf.AppendByte('\\')
				buf.AppendByte(b)
			case '\n':
				buf.AppendByte('\\')
				buf.AppendByte('n')
			case '\r':
				buf.AppendByte('\\')
				buf.AppendByte('r')
			case '\t':
				buf.AppendByte('\\')
				buf.AppendByte('t')
			default:
				// This encodes bytes < 0x20 except for \n, \r, and \t.
				buf.AppendString(`\u00`)
				buf.AppendByte(hex[b>>4])
				buf.AppendByte(hex[b&0xF])
			}
			i++
			start = i
			continue
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		if c == utf8.RuneError {
			if start < i {
				buf.AppendString(s[start:i])
			}
			buf.AppendString(`\ufffd`)
			i += size
			start = i
			continue
		}
		i += size
	}
	if start < len(s) {
		buf.AppendString(s[start:])
	}
	buf.AppendByte('"')
}
