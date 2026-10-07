package telemetry

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
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
		if json.NewDecoder(bytes.NewReader(body)).Decode(&event) != nil || strings.TrimSpace(event.Event) != EventScreenViewed {
			next.ServeHTTP(w, req)
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
			r.screenMu.Lock()
			now := time.Now().UTC()
			day := now.Format(time.DateOnly)
			if r.screenDay != day {
				r.screenDay = day
				r.screenViews = make(map[string]bool)
			}
			if !r.screenViews[screen] {
				claimed, err = r.claimScreenView(screen, now, func() error {
					return r.client.Capture(EventScreenViewed, properties)
				})
				if claimed {
					r.screenViews[screen] = true
				}
			}
			r.screenMu.Unlock()
			if err != nil {
				if !claimed {
					http.Error(w, "recording screen view failed", http.StatusInternalServerError)
					return
				}
				slog.Warn("saving accepted screen view failed", "err", err)
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
