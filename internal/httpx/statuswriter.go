package httpx

import "net/http"

// StatusWriter remembers the status code a handler wrote, and still supports
// streaming (Flush) and http.ResponseController (Unwrap). The proxies use it
// to count what they answered.
type StatusWriter struct {
	http.ResponseWriter
	status int
}

// NewStatusWriter wraps w.
func NewStatusWriter(w http.ResponseWriter) *StatusWriter { return &StatusWriter{ResponseWriter: w} }

// Status is the code written (200 when only a body was written, 0 when
// nothing was).
func (s *StatusWriter) Status() int { return s.status }

func (s *StatusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *StatusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *StatusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *StatusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
