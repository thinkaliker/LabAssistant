package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// resyncMsg tells the dashboard that this stream has a hole in it — either its buffer
// overflowed or it reconnected past the replay window — so it must re-read state from the REST
// endpoints instead of continuing on a partial view.
var resyncMsg = []byte(`{"type":"resync"}`)

// events streams the aggregate live feed (host + job updates). Each message is tagged with its
// broker sequence number as the SSE event id, so a reconnecting EventSource resumes via
// Last-Event-ID rather than silently skipping everything published while it was away.
func (d Deps) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no_stream", "streaming unsupported")
		return
	}
	sseHeaders(w)
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// Subscribe before replaying so nothing published in between is lost. The replay's highest
	// sequence number then suppresses those same messages when they arrive on the live channel.
	ch, cancel := d.Events.Subscribe()
	defer cancel()

	var last uint64
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		if after, err := strconv.ParseUint(id, 10, 64); err == nil {
			msgs, covered := d.Events.Replay(after)
			if !covered {
				writeSSEID(w, 0, resyncMsg)
			}
			for _, m := range msgs {
				writeSSEID(w, m.Seq, m.Data)
				last = m.Seq
			}
			flusher.Flush()
		}
	}

	// Messages dropped mid-stream show up as a jump in the sequence numbers below. Ones dropped
	// at the tail don't — nothing later arrives to reveal them — so also compare against the
	// broker's own position periodically. An empty channel plus a sequence behind the broker
	// can only mean this subscriber missed messages it will never be sent.
	lag := time.NewTicker(5 * time.Second)
	defer lag.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-lag.C:
			if len(ch) == 0 && last != 0 && d.Events.Seq() > last {
				writeSSEID(w, 0, resyncMsg)
				flusher.Flush()
				last = d.Events.Seq()
			}
		case m, ok := <-ch:
			if !ok {
				return
			}
			if m.Seq <= last {
				continue // already handed over by the replay above
			}
			// A jump in the sequence means this subscriber's buffer filled and the broker
			// dropped messages for it. Say so instead of letting the client act on a feed
			// that quietly lost host changes or a job's terminal state.
			if last != 0 && m.Seq != last+1 {
				writeSSEID(w, 0, resyncMsg)
			}
			writeSSEID(w, m.Seq, m.Data)
			last = m.Seq
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
	// The backlog covers everything up to the moment of Subscribe; anything already sitting on
	// the live channel is a duplicate of it, so track the last sequence the backlog accounted
	// for and skip back over it below.
	var last uint64
	for _, ev := range backlog {
		b, _ := json.Marshal(map[string]any{"type": "job_event", "payload": ev})
		writeSSE(w, b)
		last = ev.Seq
	}
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			if m.Seq <= last {
				continue
			}
			if last != 0 && m.Seq != last+1 {
				writeSSEID(w, 0, resyncMsg)
			}
			writeSSEID(w, m.Seq, m.Data)
			last = m.Seq
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

// writeSSEID writes a message tagged with its sequence number, which the browser echoes back
// as Last-Event-ID when it reconnects. seq 0 means "not resumable" (an out-of-band notice) and
// is written without an id so it can't become a client's resume point.
func writeSSEID(w http.ResponseWriter, seq uint64, data []byte) {
	if seq == 0 {
		writeSSE(w, data)
		return
	}
	fmt.Fprintf(w, "id: %d\ndata: %s\n\n", seq, data)
}
