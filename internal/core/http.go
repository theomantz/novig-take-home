package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"novig-take-home/internal/domain"
	"novig-take-home/internal/shared"
)

func NewHandler(svc *Service, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		handleStream(svc, w, r, logger)
	})
	mux.HandleFunc("/snapshot", func(w http.ResponseWriter, _ *http.Request) {
		shared.WriteJSON(w, http.StatusOK, svc.Snapshot())
	})
	mux.HandleFunc("/internal/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			shared.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		var in domain.SignalInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			shared.WriteError(w, http.StatusBadRequest, "invalid json")
			return
		}
		svc.IngestSignal(in)
		shared.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "accepted"})
	})
	mux.HandleFunc("/internal/scenarios/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			shared.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/internal/scenarios/")
		marketID := r.URL.Query().Get("market_id")
		if marketID == "" {
			marketID = svc.cfg.DefaultMarketID
		}

		switch name {
		case "spike":
			runSpikeScenario(svc, marketID)
		case "normalize":
			svc.ClearSignals(marketID)
		default:
			shared.WriteError(w, http.StatusNotFound, "unknown scenario")
			return
		}

		snap := svc.Snapshot()
		shared.WriteJSON(w, http.StatusOK, map[string]any{
			"scenario":  name,
			"market_id": marketID,
			"state":     snap.Markets[marketID],
			"last_seq":  snap.LastSeq,
		})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		shared.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mux
}

func runSpikeScenario(svc *Service, marketID string) {
	nowMs := time.Now().UnixMilli()
	for i := 0; i < 24; i++ {
		svc.IngestSignal(domain.SignalInput{
			MarketID:            marketID,
			BetCount:            6,
			StakeCents:          15_000,
			LiabilityDeltaCents: 100_000,
			TsUnixMs:            nowMs,
		})
	}
}

func handleStream(svc *Service, w http.ResponseWriter, r *http.Request, logger *slog.Logger) {
	fromSeq := int64(1)
	if raw := r.URL.Query().Get("from_seq"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 1 {
			shared.WriteError(w, http.StatusBadRequest, "invalid from_seq")
			return
		}
		fromSeq = parsed
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		shared.WriteError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	streamCh, unsubscribe := svc.Subscribe(1024)
	defer unsubscribe()

	nextSeq := fromSeq
	backlog, err := svc.EventsFromSeq(nextSeq)
	if err != nil {
		shared.WriteError(w, http.StatusInternalServerError, "failed to load backlog")
		return
	}

	// Flush headers immediately so clients can establish the SSE session without waiting for new events.
	if _, err := fmt.Fprint(w, ":connected\n\n"); err != nil {
		return
	}
	flusher.Flush()

	for _, evt := range backlog {
		if err := writeSSEEvent(w, flusher, evt); err != nil {
			logger.Warn("stream write failed", "error", err)
			return
		}
		nextSeq = evt.Seq + 1
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ":keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case evt, ok := <-streamCh:
			if !ok {
				return
			}
			if evt.Seq < nextSeq {
				continue
			}
			if evt.Seq > nextSeq {
				gapFill, gapErr := svc.EventsFromSeq(nextSeq)
				if gapErr != nil {
					logger.Warn("gap fill query failed", "error", gapErr)
					return
				}
				for _, fillEvt := range gapFill {
					if err := writeSSEEvent(w, flusher, fillEvt); err != nil {
						return
					}
					nextSeq = fillEvt.Seq + 1
				}
				if evt.Seq < nextSeq {
					continue
				}
			}
			if err := writeSSEEvent(w, flusher, evt); err != nil {
				return
			}
			nextSeq = evt.Seq + 1
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, evt domain.ReplicationEvent) error {
	payload, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: replication\n"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
