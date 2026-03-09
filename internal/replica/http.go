package replica

import (
	"net/http"
	"slices"
	"strings"

	"novig-take-home/internal/domain"
	"novig-take-home/internal/shared"
)

// NewHandler exposes read-only replica APIs for market snapshots, history, and replication status.
func NewHandler(svc *Service) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/markets", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			shared.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		markets := svc.Markets()
		slices.SortFunc(markets, func(a, b domain.MarketState) int {
			switch {
			case a.MarketID < b.MarketID:
				return -1
			case a.MarketID > b.MarketID:
				return 1
			default:
				return 0
			}
		})
		shared.WriteJSON(w, http.StatusOK, map[string]any{"markets": markets})
	})

	mux.HandleFunc("/markets/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			shared.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		remainder := strings.TrimPrefix(r.URL.Path, "/markets/")
		if remainder == "" {
			shared.WriteError(w, http.StatusNotFound, "not found")
			return
		}

		parts := strings.Split(remainder, "/")
		marketID := parts[0]
		if marketID == "" {
			shared.WriteError(w, http.StatusNotFound, "not found")
			return
		}

		if len(parts) == 2 && parts[1] == "history" {
			shared.WriteJSON(w, http.StatusOK, map[string]any{"market_id": marketID, "history": svc.MarketHistory(marketID)})
			return
		}
		if len(parts) > 1 {
			shared.WriteError(w, http.StatusNotFound, "not found")
			return
		}

		market, ok := svc.MarketByID(marketID)
		if !ok {
			shared.WriteError(w, http.StatusNotFound, "market not found")
			return
		}
		shared.WriteJSON(w, http.StatusOK, market)
	})

	mux.HandleFunc("/replica/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			shared.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		shared.WriteJSON(w, http.StatusOK, svc.Status())
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		shared.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	return mux
}
