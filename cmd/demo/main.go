package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"novig-take-home/internal/core"
	"novig-take-home/internal/domain"
	"novig-take-home/internal/replica"
)

const defaultMarketID = "market-news-001"

// main runs an end-to-end convergence demo against one core and two replicas.
func main() {
	hold := flag.Bool("hold", false, "keep services running after scripted checks")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	coreStore, err := core.NewEventStore(core.InMemoryDSN("core_events"))
	if err != nil {
		logger.Error("init core store failed", "error", err)
		os.Exit(1)
	}
	defer coreStore.Close()

	coreSvc, err := core.NewService(coreStore, core.ServiceConfig{
		Breaker:         domain.DefaultBreakerConfig(),
		TickInterval:    time.Second,
		WindowDuration:  30 * time.Second,
		DefaultMarketID: defaultMarketID,
		Logger:          logger,
	})
	if err != nil {
		logger.Error("init core service failed", "error", err)
		os.Exit(1)
	}
	go coreSvc.Start(ctx)

	coreServer, coreURL, err := startHTTPServer(core.NewHandler(coreSvc, logger))
	if err != nil {
		logger.Error("start core server failed", "error", err)
		os.Exit(1)
	}
	defer shutdownServer(coreServer)

	replica1, replica1URL, err := startReplica(ctx, "r1", coreURL, logger)
	if err != nil {
		logger.Error("start replica1 failed", "error", err)
		os.Exit(1)
	}
	defer replica1.store.Close()
	defer shutdownServer(replica1.server)

	replica2, replica2URL, err := startReplica(ctx, "r2", coreURL, logger)
	if err != nil {
		logger.Error("start replica2 failed", "error", err)
		os.Exit(1)
	}
	defer replica2.store.Close()
	defer shutdownServer(replica2.server)

	fmt.Printf("core: %s\nreplica1: %s\nreplica2: %s\n", coreURL, replica1URL, replica2URL)

	client := &http.Client{Timeout: 5 * time.Second}
	if err := waitReplicaConnected(ctx, client, replica1URL, 10*time.Second); err != nil {
		logger.Error("replica1 did not connect", "error", err)
		os.Exit(1)
	}
	if err := waitReplicaConnected(ctx, client, replica2URL, 10*time.Second); err != nil {
		logger.Error("replica2 did not connect", "error", err)
		os.Exit(1)
	}

	if err := post(client, coreURL+"/internal/scenarios/spike"); err != nil {
		logger.Error("failed to trigger spike", "error", err)
		os.Exit(1)
	}
	if err := waitStatus(ctx, client, replica1URL, defaultMarketID, domain.MarketStatusSuspended, 12*time.Second); err != nil {
		logger.Error("replica1 did not suspend", "error", err)
		os.Exit(1)
	}
	if err := waitStatus(ctx, client, replica2URL, defaultMarketID, domain.MarketStatusSuspended, 12*time.Second); err != nil {
		logger.Error("replica2 did not suspend", "error", err)
		os.Exit(1)
	}
	fmt.Println("verified: both replicas converged to SUSPENDED")

	if err := post(client, coreURL+"/internal/scenarios/normalize"); err != nil {
		logger.Error("failed to trigger normalize", "error", err)
		os.Exit(1)
	}

	if err := waitStatus(ctx, client, replica1URL, defaultMarketID, domain.MarketStatusOpen, 75*time.Second); err != nil {
		logger.Error("replica1 did not reopen", "error", err)
		os.Exit(1)
	}
	if err := waitStatus(ctx, client, replica2URL, defaultMarketID, domain.MarketStatusOpen, 75*time.Second); err != nil {
		logger.Error("replica2 did not reopen", "error", err)
		os.Exit(1)
	}

	fmt.Println("verified: both replicas converged back to OPEN after cooldown")
	if !*hold {
		return
	}

	fmt.Println("hold mode enabled; services remain running until interrupted")
	<-ctx.Done()
}

type runningReplica struct {
	store  *replica.Store
	server *http.Server
}

// startReplica provisions a replica store/service pair and serves its HTTP read API.
func startReplica(ctx context.Context, id string, coreURL string, logger *slog.Logger) (*runningReplica, string, error) {
	dsn := replica.InMemoryDSN(id)
	store, err := replica.NewStore(dsn)
	if err != nil {
		return nil, "", err
	}

	svc, err := replica.NewService(store, replica.ServiceConfig{
		ID:               id,
		CoreBaseURL:      coreURL,
		Logger:           logger,
		ReconnectBackoff: 250 * time.Millisecond,
	})
	if err != nil {
		_ = store.Close()
		return nil, "", err
	}

	go svc.Start(ctx)

	srv, baseURL, err := startHTTPServer(replica.NewHandler(svc))
	if err != nil {
		_ = store.Close()
		return nil, "", err
	}

	return &runningReplica{store: store, server: srv}, baseURL, nil
}

// startHTTPServer starts handler on a random localhost port and returns its base URL.
func startHTTPServer(handler http.Handler) (*http.Server, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	srv := &http.Server{Handler: handler}
	go func() {
		_ = srv.Serve(ln)
	}()
	return srv, "http://" + ln.Addr().String(), nil
}

// shutdownServer performs a bounded graceful shutdown for demo HTTP servers.
func shutdownServer(srv *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// post issues a POST with no body and enforces a 2xx response.
func post(client *http.Client, target string) error {
	req, err := http.NewRequest(http.MethodPost, target, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	return nil
}

// waitReplicaConnected polls /replica/status until Connected becomes true or timeout elapses.
func waitReplicaConnected(ctx context.Context, client *http.Client, baseURL string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return errors.New("timeout waiting for replica connection")
		case <-ticker.C:
			status, err := getReplicaStatus(client, baseURL)
			if err == nil && status.Connected {
				return nil
			}
		}
	}
}

// waitStatus polls a replica market until it reaches target status or the timeout expires.
func waitStatus(ctx context.Context, client *http.Client, replicaURL string, marketID string, target domain.MarketStatus, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for status %s", target)
		case <-ticker.C:
			market, err := getMarket(client, replicaURL, marketID)
			if err == nil && market.Status == target {
				return nil
			}
		}
	}
}

// getMarket fetches a replica market snapshot and decodes it as MarketState.
func getMarket(client *http.Client, baseURL string, marketID string) (domain.MarketState, error) {
	resp, err := client.Get(baseURL + "/markets/" + marketID)
	if err != nil {
		return domain.MarketState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return domain.MarketState{}, fmt.Errorf("status=%d", resp.StatusCode)
	}
	var market domain.MarketState
	if err := json.NewDecoder(resp.Body).Decode(&market); err != nil {
		return domain.MarketState{}, err
	}
	return market, nil
}

// getReplicaStatus fetches and decodes the replica liveness/lag status document.
func getReplicaStatus(client *http.Client, baseURL string) (replica.Status, error) {
	resp, err := client.Get(baseURL + "/replica/status")
	if err != nil {
		return replica.Status{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return replica.Status{}, fmt.Errorf("status=%d", resp.StatusCode)
	}
	var status replica.Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return replica.Status{}, err
	}
	return status, nil
}
