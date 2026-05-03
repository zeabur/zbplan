package builder

import (
	"io"
	"log/slog"
)

type slogWriter struct {
	logger *slog.Logger
	prefix string
}

func NewSlogWriter(logger *slog.Logger, prefix string) io.Writer {
	return &slogWriter{
		logger: logger,
		prefix: prefix,
	}
}

func (w *slogWriter) Write(p []byte) (n int, err error) {
	w.logger.Info(w.prefix, slog.String("output", string(p)))
	return len(p), nil
}
