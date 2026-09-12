package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"workbuddy2api/internal/requestlog"
	"workbuddy2api/internal/upstream"
)

type requestLogKey struct{}

// requestRecord returns an isolated record for direct handler calls in tests too.
func requestRecord(r *http.Request) *requestlog.Record {
	if record, ok := r.Context().Value(requestLogKey{}).(*requestlog.Record); ok {
		return record
	}
	return &requestlog.Record{}
}

// captureRequest surrounds authentication too, but never reads headers or bodies into the log.
func (h *Handler) captureRequest(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		record := &requestlog.Record{StartedAt: time.Now(), CompletionTokens: -1}
		rw := &logResponseWriter{ResponseWriter: w}
		completed := false
		defer func() {
			record.FinishedAt = time.Now()
			record.DurationMS = time.Since(record.StartedAt).Milliseconds()
			record.Status = rw.status
			if !completed {
				record.ErrorCode = "internal_error"
			} else if r.Context().Err() != nil || rw.err != nil {
				record.ErrorCode = "cancelled"
			}
			if record.ErrorCode == "" {
				switch rw.status {
				case http.StatusUnauthorized:
					record.ErrorCode = "invalid_api_key"
				case http.StatusBadRequest:
					record.ErrorCode = "invalid_request"
				case http.StatusRequestEntityTooLarge:
					record.ErrorCode = "request_body_too_large"
				}
			}
			record.Result = "success"
			if record.ErrorCode != "" || record.Status < 200 || record.Status >= 400 {
				record.Result = "failed"
				if record.ErrorCode == "" {
					record.ErrorCode = "internal_error"
				}
			}
			if record.Attempts > 0 {
				record.Retries = record.Attempts - 1
			}
			if n := len(record.AttemptDetails); n > 0 && record.AttemptDetails[n-1].ErrorCode == "" && record.ErrorCode != "" {
				record.NoteFailure(record.ErrorCode)
			}
			h.cfg.RequestLogs.Add(*record)
		}()
		next(rw, r.WithContext(context.WithValue(r.Context(), requestLogKey{}, record)))
		completed = true
	}
}

// Preserve streaming and report the actual downstream HTTP status, not upstream status.
type logResponseWriter struct {
	http.ResponseWriter
	status int
	err    error
}

func (w *logResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *logResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 100 && status < 200 {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *logResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	if err != nil {
		w.err = err
	}
	return n, err
}
func (w *logResponseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if err := http.NewResponseController(w.ResponseWriter).Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		w.err = err
	}
}

func requestErrorCode(err error, fallback string) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return "timeout"
	}
	var ue *upstream.Error
	if errors.As(err, &ue) && ue.Kind != upstream.ErrNone {
		return ue.Kind.String()
	}
	return fallback
}
