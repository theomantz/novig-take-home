package core

import (
	"fmt"
	"sort"
)

type ReplicaHealthClass string

const (
	ReplicaHealthHealthy ReplicaHealthClass = "healthy"
	ReplicaHealthStale   ReplicaHealthClass = "stale"
	ReplicaHealthOffline ReplicaHealthClass = "offline"
)

type ReplicaHeartbeat struct {
	ReplicaID      string `json:"replica_id"`
	Connected      bool   `json:"connected"`
	LastAppliedSeq int64  `json:"last_applied_seq"`
	CoreLastSeq    int64  `json:"core_last_seq"`
	LastSyncAt     int64  `json:"last_sync_at"`
}

type ReplicaStatus struct {
	ReplicaID           string             `json:"replica_id"`
	Health              ReplicaHealthClass `json:"health"`
	Connected           bool               `json:"connected"`
	LastAppliedSeq      int64              `json:"last_applied_seq"`
	ReportedCoreLastSeq int64              `json:"reported_core_last_seq"`
	LagSeq              int64              `json:"lag_seq"`
	LastSyncAt          int64              `json:"last_sync_at"`
	LastSeenAt          int64              `json:"last_seen_at"`
}

type ReplicasStatusResponse struct {
	GeneratedAtMs  int64           `json:"generated_at_ms"`
	CoreLastSeq    int64           `json:"core_last_seq"`
	StaleAfterMs   int64           `json:"stale_after_ms"`
	OfflineAfterMs int64           `json:"offline_after_ms"`
	Replicas       []ReplicaStatus `json:"replicas"`
}

type replicaHealthRecord struct {
	ReplicaHeartbeat
	LastSeenAtMs int64
	Health       ReplicaHealthClass
}

func classifyReplicaHealth(connected bool, ageMs int64, staleAfterMs int64, offlineAfterMs int64) ReplicaHealthClass {
	if !connected {
		return ReplicaHealthOffline
	}
	if ageMs < 0 {
		ageMs = 0
	}
	if ageMs >= offlineAfterMs {
		return ReplicaHealthOffline
	}
	if ageMs >= staleAfterMs {
		return ReplicaHealthStale
	}
	return ReplicaHealthHealthy
}

func sortReplicaStatuses(statuses []ReplicaStatus) {
	sort.Slice(statuses, func(i, j int) bool {
		return statuses[i].ReplicaID < statuses[j].ReplicaID
	})
}

func replicaTransitionLabel(prev ReplicaHealthClass) string {
	if prev == "" {
		return "unknown"
	}
	return string(prev)
}

func formatReplicaTransition(prev ReplicaHealthClass, next ReplicaHealthClass) string {
	return fmt.Sprintf("%s->%s", replicaTransitionLabel(prev), next)
}
