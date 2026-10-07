package telemetry

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

func (r *Reporter) screenViewHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || !r.Enabled() {
			next.ServeHTTP(w, req)
			return
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, "invalid telemetry request", http.StatusBadRequest)
			return
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
		var event struct {
			Event      string         `json:"event"`
			Properties map[string]any `json:"properties"`
		}
		decoder := json.NewDecoder(bytes.NewReader(body))
		if decoder.Decode(&event) != nil || strings.TrimSpace(event.Event) != EventScreenViewed {
			next.ServeHTTP(w, req)
			return
		}
		contentType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
		if err != nil || contentType != "application/json" {
			http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
			return
		}
		if decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid telemetry request", http.StatusBadRequest)
			return
		}
		properties, err := r.SanitizeProperties(event.Event, event.Properties)
		if err != nil {
			next.ServeHTTP(w, req)
			return
		}
		screen, valid := properties["screen"].(string)
		claimed := false
		if valid && r.claimScreenView != nil {
			claimed, err = r.claimScreenView(screen, time.Now().UTC(), func() error {
				return r.client.Capture(EventScreenViewed, properties)
			})
			if err != nil {
				http.Error(w, "recording screen view failed", http.StatusInternalServerError)
				return
			}
		}
		status := "dropped"
		if claimed {
			status = "queued"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
	})
}
