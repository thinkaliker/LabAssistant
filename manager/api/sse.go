package api

import (
	"encoding/json"
	"fmt"
	"net/http"
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
