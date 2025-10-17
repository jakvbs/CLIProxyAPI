package logging

import (
    "bytes"
    "fmt"
    "io"
    "os"
    "path/filepath"
    "sort"
    "strings"
    "sync"

    "github.com/gin-gonic/gin"
    log "github.com/sirupsen/logrus"
    "gopkg.in/natefinch/lumberjack.v2"
)

var (
	setupOnce      sync.Once
	writerMu       sync.Mutex
	logWriter      *lumberjack.Logger
	ginInfoWriter  *io.PipeWriter
	ginErrorWriter *io.PipeWriter
)

// LogFormatter defines a custom log format for logrus.
// This formatter adds timestamp, level, and source location to each log entry.
type LogFormatter struct{}

// Format renders a single log entry with custom formatting.
func (m *LogFormatter) Format(entry *log.Entry) ([]byte, error) {
	var buffer *bytes.Buffer
	if entry.Buffer != nil {
		buffer = entry.Buffer
	} else {
		buffer = &bytes.Buffer{}
	}

	timestamp := entry.Time.Format("2006-01-02 15:04:05")
	message := strings.TrimRight(entry.Message, "\r\n")
    formatted := fmt.Sprintf("[%s] [%s] [%s:%d] %s", timestamp, entry.Level, filepath.Base(entry.Caller.File), entry.Caller.Line, message)
    buffer.WriteString(formatted)

    // Render structured fields as key=value pairs (sorted for stability).
    if len(entry.Data) > 0 {
        keys := make([]string, 0, len(entry.Data))
        for k := range entry.Data {
            keys = append(keys, k)
        }
        sort.Strings(keys)
        for _, k := range keys {
            v := entry.Data[k]
            // Coerce to string while trimming newlines to keep one-line logs.
            s := strings.TrimSpace(fmt.Sprintf("%v", v))
            s = strings.ReplaceAll(s, "\n", "\\n")
            buffer.WriteString(" ")
            buffer.WriteString(k)
            buffer.WriteString("=")
            buffer.WriteString(s)
        }
    }

    buffer.WriteString("\n")

    return buffer.Bytes(), nil
}

// SetupBaseLogger configures the shared logrus instance and Gin writers.
// It is safe to call multiple times; initialization happens only once.
func SetupBaseLogger() {
	setupOnce.Do(func() {
		log.SetOutput(os.Stdout)
		log.SetReportCaller(true)
		log.SetFormatter(&LogFormatter{})

		ginInfoWriter = log.StandardLogger().Writer()
		gin.DefaultWriter = ginInfoWriter
		ginErrorWriter = log.StandardLogger().WriterLevel(log.ErrorLevel)
		gin.DefaultErrorWriter = ginErrorWriter
		gin.DebugPrintFunc = func(format string, values ...interface{}) {
			format = strings.TrimRight(format, "\r\n")
			log.StandardLogger().Infof(format, values...)
		}

		log.RegisterExitHandler(closeLogOutputs)
	})
}

// ConfigureLogOutput switches the global log destination between rotating files and stdout.
func ConfigureLogOutput(loggingToFile bool) error {
	SetupBaseLogger()

	writerMu.Lock()
	defer writerMu.Unlock()

	if loggingToFile {
		const logDir = "logs"
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return fmt.Errorf("logging: failed to create log directory: %w", err)
		}
		if logWriter != nil {
			_ = logWriter.Close()
		}
		logWriter = &lumberjack.Logger{
			Filename:   filepath.Join(logDir, "main.log"),
			MaxSize:    10,
			MaxBackups: 0,
			MaxAge:     0,
			Compress:   false,
		}
		log.SetOutput(logWriter)
		return nil
	}

	if logWriter != nil {
		_ = logWriter.Close()
		logWriter = nil
	}
	log.SetOutput(os.Stdout)
	return nil
}

func closeLogOutputs() {
	writerMu.Lock()
	defer writerMu.Unlock()

	if logWriter != nil {
		_ = logWriter.Close()
		logWriter = nil
	}
	if ginInfoWriter != nil {
		_ = ginInfoWriter.Close()
		ginInfoWriter = nil
	}
	if ginErrorWriter != nil {
		_ = ginErrorWriter.Close()
		ginErrorWriter = nil
	}
}
