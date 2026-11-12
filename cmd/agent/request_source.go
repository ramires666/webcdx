package main

import "log"

func logSourcef(source, format string, args ...any) {
	if source != "" {
		format = "[source=" + source + "] " + format
	}
	log.Printf(format, args...)
}

func (e *nativeExecutor) logf(format string, args ...any) {
	logSourcef(e.source, format, args...)
}

func (w *cappedStreamWriter) logf(format string, args ...any) {
	logSourcef(w.source, format, args...)
}
