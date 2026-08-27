package api

import (
	"context"
	"net/http"

	"github.com/N0rthHXD/dMem/internal/config"
	"github.com/N0rthHXD/dMem/internal/storage"
)

type configurationStatus struct {
	State                string                    `json:"state"`
	ActivationGeneration int                       `json:"activation_generation"`
	ActiveSourceIdentity string                    `json:"active_source_identity"`
	ActiveEffectiveID    string                    `json:"active_effective_identity"`
	ActivatedAt          string                    `json:"activated_at"`
	LastAttempt          configurationAttempt      `json:"last_attempt"`
	Capabilities         configurationCapabilities `json:"capabilities"`
}

type configurationCapabilities struct {
	Processing config.RedactedCapability `json:"processing"`
	Embedding  config.RedactedCapability `json:"embedding"`
}

type configurationAttempt struct {
	AttemptID   string                   `json:"attempt_id"`
	Trigger     config.ActivationTrigger `json:"trigger"`
	Outcome     config.ActivationOutcome `json:"outcome"`
	SourceID    string                   `json:"source_identity,omitempty"`
	ErrorCode   string                   `json:"error_code,omitempty"`
	StartedAt   string                   `json:"started_at"`
	FinishedAt  string                   `json:"finished_at"`
	Diagnostics []config.Diagnostic      `json:"diagnostics"`
}

type reloadConfigurationRequest struct {
	ExpectedEffectiveIdentity *string `json:"expected_effective_identity"`
}

type reloadConfigurationResponse struct {
	AttemptID                 string                   `json:"attempt_id"`
	Outcome                   config.ActivationOutcome `json:"outcome"`
	PreviousEffectiveIdentity string                   `json:"previous_effective_identity,omitempty"`
	ActiveSourceIdentity      string                   `json:"active_source_identity"`
	ActiveEffectiveIdentity   string                   `json:"active_effective_identity"`
	ChangedCapabilities       []string                 `json:"changed_capabilities"`
	ReconciliationState       string                   `json:"reconciliation_state"`
}

func (h *Handler) configurationStatus(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	if _, ok := h.requireCaller(w, r, requestID); !ok {
		return
	}
	if h.configuration == nil {
		h.writeError(w, http.StatusServiceUnavailable, "config_unmanaged", "configuration manager is unavailable", requestID, "", true, nil)
		return
	}
	snapshot, attempt := h.configuration.ConfigurationStatus()
	if snapshot == nil {
		h.writeError(w, http.StatusServiceUnavailable, "config_unavailable", "no active configuration", requestID, "", true, nil)
		return
	}
	redacted := snapshot.Redacted()
	states, err := h.repository.CurrentCapabilityStates(r.Context())
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "config_status_unavailable", "configuration status is unavailable", requestID, "", true, nil)
		return
	}
	processing := redacted.Processing
	embedding := redacted.Embedding
	mergeCapabilityState(&processing, states["processing"])
	mergeCapabilityState(&embedding, states["embedding"])
	diagnostics := attempt.Diagnostics
	if diagnostics == nil {
		diagnostics = []config.Diagnostic{}
	}
	h.writeJSON(w, http.StatusOK, configurationStatus{
		State: "active", ActivationGeneration: snapshot.ActivationGeneration,
		ActiveSourceIdentity: snapshot.Config.SourceIdentity, ActiveEffectiveID: snapshot.Config.EffectiveIdentity,
		ActivatedAt:  snapshot.ActivatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		LastAttempt:  configurationAttempt{AttemptID: attempt.AttemptID, Trigger: attempt.Trigger, Outcome: attempt.Outcome, SourceID: attempt.SourceIdentity, ErrorCode: attempt.ErrorCode, StartedAt: attempt.StartedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), FinishedAt: attempt.FinishedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), Diagnostics: diagnostics},
		Capabilities: configurationCapabilities{Processing: processing, Embedding: embedding},
	})
}

func mergeCapabilityState(capability *config.RedactedCapability, state storage.CapabilityStateRecord) {
	if state.Capability == "" {
		return
	}
	capability.State = state.HealthState
	capability.ConfigurationGeneration = state.ConfigurationGeneration
	capability.PolicyGeneration = state.PolicyGeneration
	capability.BlockedWorkCount = state.BlockedWorkCount
	capability.ReconciliationState = state.ReconciliationState
}

func (h *Handler) reloadConfiguration(w http.ResponseWriter, r *http.Request) {
	requestID := h.requestID()
	caller, ok := h.requireCaller(w, r, requestID)
	if !ok {
		return
	}
	key, ok := h.requireIdempotency(w, r, requestID)
	if !ok {
		return
	}
	var request reloadConfigurationRequest
	if !h.decode(w, r, requestID, &request) {
		return
	}
	if h.configuration == nil {
		h.writeError(w, http.StatusServiceUnavailable, "config_unmanaged", "configuration manager is unavailable", requestID, "", true, nil)
		return
	}
	expectedIdentity := ""
	if request.ExpectedEffectiveIdentity != nil {
		expectedIdentity = *request.ExpectedEffectiveIdentity
	}
	requestKey := caller + "\x00" + key
	h.reloadMu.Lock()
	defer h.reloadMu.Unlock()
	if previous, exists := h.reloadRequests[requestKey]; exists {
		if previous.expectedIdentity != expectedIdentity {
			h.writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency key was used for a different request", requestID, "", false, nil)
			return
		}
		w.Header().Set("Idempotent-Replay", "true")
		h.writeJSON(w, http.StatusOK, previous.response)
		return
	}
	before, _ := h.configuration.ConfigurationStatus()
	if request.ExpectedEffectiveIdentity != nil && (before == nil || *request.ExpectedEffectiveIdentity != before.Config.EffectiveIdentity) {
		h.writeError(w, http.StatusConflict, "config_identity_conflict", "active configuration identity does not match", requestID, "", false, nil)
		return
	}
	result, err := h.configuration.ReloadConfiguration(context.Background())
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "config_reload_failed", "configuration reload failed", requestID, "", true, nil)
		return
	}
	if result.Outcome == config.OutcomeRejected || result.Outcome == config.OutcomeFailed {
		h.writeError(w, http.StatusUnprocessableEntity, result.ErrorCode, "configuration was rejected", requestID, "", false, map[string]any{"diagnostics": result.Diagnostics})
		return
	}
	snapshot, _ := h.configuration.ConfigurationStatus()
	changedCapabilities := result.ChangedCapabilities
	if changedCapabilities == nil {
		changedCapabilities = []string{}
	}
	reconciliationState := "queued"
	if result.Outcome == config.OutcomeNoChange || len(result.ReconciliationClasses) == 0 {
		reconciliationState = "idle"
	}
	response := reloadConfigurationResponse{AttemptID: result.AttemptID, Outcome: result.Outcome, PreviousEffectiveIdentity: result.PreviousEffectiveIdentity, ActiveSourceIdentity: snapshot.Config.SourceIdentity, ActiveEffectiveIdentity: snapshot.Config.EffectiveIdentity, ChangedCapabilities: changedCapabilities, ReconciliationState: reconciliationState}
	h.reloadRequests[requestKey] = reloadRequest{expectedIdentity: expectedIdentity, response: response}
	w.Header().Set("Idempotent-Replay", "false")
	h.writeJSON(w, http.StatusOK, response)
}
