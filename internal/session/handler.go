package session

import (
	"encoding/json"
	"net/http"

	"github.com/jxman78/wiki-brain/internal/foundation"
)

type Handler struct {
	store  *Store
	parser *Parser
}

func NewHandler(store *Store, parser *Parser) *Handler {
	return &Handler{store: store, parser: parser}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /sessions", h.listSessions)
	mux.HandleFunc("POST /sessions", h.createSession)
	mux.HandleFunc("DELETE /sessions/{id}", h.deleteSession)
	mux.HandleFunc("GET /sessions/{id}/turns", h.listTurns)
	mux.HandleFunc("POST /session/turn", h.postTurn)
	mux.HandleFunc("POST /session/clarify", h.postClarify)
	mux.HandleFunc("POST /session/working", h.postWorking)
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.ListSessions()
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []SessionInfo{}
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{"sessions": list})
}

func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) {
	info, err := h.store.CreateSession()
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	foundation.WriteJSON(w, http.StatusCreated, info)
}

func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.Delete(id); err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listTurns(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	turns, err := h.store.ListTurns(id)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if turns == nil {
		turns = []TurnInfo{}
	}
	foundation.WriteJSON(w, http.StatusOK, map[string]interface{}{"turns": turns})
}

func (h *Handler) postTurn(w http.ResponseWriter, r *http.Request) {
	var input TurnInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if input.SessionID == "" || input.UserInput == "" {
		foundation.WriteError(w, http.StatusBadRequest, "session_id and user_input required")
		return
	}

	state, err := h.store.Get(input.SessionID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	titleRunes := []rune(input.UserInput)
	if len(titleRunes) > 30 {
		titleRunes = titleRunes[:30]
	}
	h.store.UpdateTitle(input.SessionID, string(titleRunes))

	result := TurnResult{SessionID: input.SessionID}

	// 1. Interrupt
	if DetectInterrupt(input.UserInput) {
		*state = SessionState{}
		result.Action = "interrupted"
		h.store.Set(input.SessionID, state)
		h.store.InsertTurn(input.SessionID, input.UserInput, "interrupted", "")
		foundation.WriteJSON(w, http.StatusOK, result)
		return
	}

	// 2. Continuation
	if DetectContinuation(input.UserInput, state) {
		eq := Expand(state, PlanResult{Action: PlanRetrieve}, input.UserInput)
		eq.ExpandedQuestion = state.Working.ContinuableAction
		result.Action = "retrieve"
		result.ExpandedQuery = &eq
		h.store.Set(input.SessionID, state)
		h.store.InsertTurn(input.SessionID, input.UserInput, "retrieve", "")
		foundation.WriteJSON(w, http.StatusOK, result)
		return
	}

	// 3. SessionParser (LLM)
	parsed := h.parser.Parse(r.Context(), input.UserInput, state)

	// 4. Update state
	state.Dialogue.Intent = parsed.Intent
	state.Dialogue.Subject = parsed.Subject
	if parsed.Subject != "" {
		state.Working.CurrentSubject = parsed.Subject
		state.Dialogue.RecentSubjects = append(state.Dialogue.RecentSubjects, parsed.Subject)
		if len(state.Dialogue.RecentSubjects) > 3 {
			state.Dialogue.RecentSubjects = state.Dialogue.RecentSubjects[len(state.Dialogue.RecentSubjects)-3:]
		}
	}
	UpdateTopic(state)

	// 5. Gap detection
	gaps := DetectGaps(parsed, state, input.UserInput)

	// 6. Plan
	plan := Plan(gaps, parsed, state)

	switch plan.Action {
	case PlanRetrieve:
		eq := Expand(state, plan, input.UserInput)
		result.Action = "retrieve"
		result.ExpandedQuery = &eq
		h.store.Set(input.SessionID, state)
		h.store.InsertTurn(input.SessionID, input.UserInput, "retrieve", "")

	case PlanClarify:
		result.Action = "clarify"
		result.Clarification = plan.Clarification

		var optionRefs []string
		if plan.Clarification != nil {
			for _, o := range plan.Clarification.Options {
				optionRefs = append(optionRefs, o.Ref)
			}
		}
		turnIdx := len(state.Dialogue.ClarificationLog) + 1
		state.Dialogue.ClarificationLog = append(state.Dialogue.ClarificationLog, ClarificationRecord{
			Question: plan.Clarification.Question,
			Options:  optionRefs,
			Response: "pending",
			Turn:     turnIdx,
		})
		clarifyMsg := ""
		if plan.Clarification != nil {
			clarifyMsg = plan.Clarification.Question
		}
		h.store.Set(input.SessionID, state)
		h.store.InsertTurn(input.SessionID, input.UserInput, "clarify", clarifyMsg)

	case PlanSkip:
		eq := Expand(state, plan, input.UserInput)
		result.Action = "retrieve"
		result.ExpandedQuery = &eq
		h.store.Set(input.SessionID, state)
		h.store.InsertTurn(input.SessionID, input.UserInput, "retrieve", "")
	}

	result.StateSnapshot = state
	foundation.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) postClarify(w http.ResponseWriter, r *http.Request) {
	var input ClarifyInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	state, err := h.store.Get(input.SessionID)
	if err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	result := ClarifyResult{}

	if input.SelectedRef != "" {
		state.Working.CurrentSubject = input.SelectedRef
		state.Dialogue.Subject = input.SelectedRef
		UpdateTopic(state)

		if len(state.Dialogue.ClarificationLog) > 0 {
			state.Dialogue.ClarificationLog[len(state.Dialogue.ClarificationLog)-1].Response = "selected:" + input.SelectedRef
		}

		eq := Expand(state, PlanResult{Action: PlanRetrieve, Subject: input.SelectedRef}, "")
		result.Action = "retrieve"
		result.ExpandedQuery = &eq
	} else {
		if len(state.Dialogue.ClarificationLog) > 0 {
			state.Dialogue.ClarificationLog[len(state.Dialogue.ClarificationLog)-1].Response = "refused"
		}

		if state.Working.CurrentSubject != "" {
			eq := Expand(state, PlanResult{Action: PlanRetrieve, Subject: state.Working.CurrentSubject}, "")
			result.Action = "retrieve"
			result.ExpandedQuery = &eq
		} else {
			result.Action = "skip"
		}
	}

	h.store.Set(input.SessionID, state)
	foundation.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) postWorking(w http.ResponseWriter, r *http.Request) {
	var input WorkingInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		foundation.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := h.store.UpdateWorking(input.SessionID, input.StepSummary, input.ContinuableAction); err != nil {
		foundation.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
