package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// events streams the aggregate live feed (host + job updates).
func (d Deps) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no_stream", "streaming unsupported")
		return
	}
	sseHeaders(w)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ch, cancel := d.Events.Subscribe()
	defer cancel()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, msg)
			flusher.Flush()
		}
	}
}

// jobEvents streams one job's buffered + live progress/log events.
func (d Deps) jobEvents(w http.ResponseWriter, r *http.Request) {
	j, ok := d.Jobs.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "job not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no_stream", "streaming unsupported")
		return
	}
	sseHeaders(w)

	backlog, ch, cancel := j.Subscribe()
	defer cancel()
	for _, ev := range backlog {
		b, _ := json.Marshal(map[string]any{"type": "job_event", "payload": ev})
		writeSSE(w, b)
	}
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, msg)
			flusher.Flush()
		}
	}
}

// hostLogs streams logs from a module on a host (e.g. ?module=duo&stack=media&service=jellyfin).
func (d Deps) hostLogs(w http.ResponseWriter, r *http.Request) {
	moduleName := r.URL.Query().Get("module")
	if moduleName == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "module query parameter is required")
		return
	}
	params := map[string]string{}
	for k, v := range r.URL.Query() {
		if k == "module" || len(v) == 0 {
			continue
		}
		params[k] = v[0]
	}
	pj, _ := json.Marshal(params)

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no_stream", "streaming unsupported")
		return
	}
	_, ch, cancel, err := d.Hub.OpenLogStream(r.PathValue("id"), moduleName, pj)
	if err != nil {
		writeErr(w, http.StatusConflict, "offline", err.Error())
		return
	}
	defer cancel()
	sseHeaders(w)
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, line)
			flusher.Flush()
		}
	}
}

// managerUpdateLogs tails the manager self-update log file and streams its lines, so the
// dashboard can surface update progress in the jobs panel. The connection ends naturally
// when the manager restarts at the end of the update (the process — and this stream — dies).
func (d Deps) managerUpdateLogs(w http.ResponseWriter, r *http.Request) {
	if d.UpdateLogPath == "" {
		writeErr(w, http.StatusNotImplemented, "unsupported", "self-update is not available")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no_stream", "streaming unsupported")
		return
	}
	sseHeaders(w)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ctx := r.Context()
	emit := func(kind, message string) {
		b, _ := json.Marshal(map[string]any{"kind": kind, "message": message})
		writeSSE(w, b)
		flusher.Flush()
	}

	// The update POST truncates/creates the log; this stream may open just before or after.
	// Wait briefly for the file to appear.
	var f *os.File
	for i := 0; i < 50; i++ {
		if ff, err := os.Open(d.UpdateLogPath); err == nil {
			f = ff
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
	if f == nil {
		emit("log", "waiting for update output timed out")
		return
	}
	defer f.Close()

	// Tail the file: read complete lines, and when we hit EOF wait for the update process to
	// append more (or for the manager to restart, which drops this connection).
	rd := bufio.NewReader(f)
	var partial string
	for {
		line, err := rd.ReadString('\n')
		partial += line
		if strings.HasSuffix(partial, "\n") {
			emit("log", strings.TrimRight(partial, "\r\n"))
			partial = ""
		}
		if err == io.EOF {
			select {
			case <-ctx.Done():
				return
			case <-time.After(400 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			return
		}
	}
}

func sseHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
}

func writeSSE(w http.ResponseWriter, data []byte) {
	fmt.Fprintf(w, "data: %s\n\n", data)
}
