package order

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
)

// Handler is the HTTP layer for orders. It parses requests, calls the
// Service, and turns results and errors into HTTP responses.
type Handler struct {
	svc *Service
	log *slog.Logger
}

// NewHandler creates a Handler that uses svc for business logic and log
// for reporting unexpected errors.
func NewHandler(svc *Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// Register adds the order routes to mux:
//
//	POST /v1/orders       -> createOrder
//	GET  /v1/orders/{id}  -> getOrder
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/orders", h.createOrder)
	mux.HandleFunc("GET /v1/orders/{id}", h.getOrder)
}

// userFromRequest returns the caller's user ID from the X-User-ID header.
// It returns an error if the header is missing or is not a valid UUID.
//
// This is a placeholder for real authentication; JWT will replace it.
func userFromRequest(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(r.Header.Get("X-User-ID"))
}

// createOrder handles POST /v1/orders.
//
// It checks the X-User-ID and Idempotency-Key headers, decodes the JSON
// body (max 1 MB, unknown fields rejected), and passes everything to
// Service.CreateOrder. The stored response is written back as-is; if it
// is a replay of an earlier request, the header Idempotent-Replayed: true
// is added.
func (h *Handler) createOrder(w http.ResponseWriter, r *http.Request) {
	userID, err := userFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "missing or invalid X-User-ID")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" || len(key) > 255 {
		writeError(w, http.StatusBadRequest, "Idempotency-Key header is required (max 255 chars)")
		return
	}

	var req CreateOrderRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	res, err := h.svc.CreateOrder(r.Context(), userID, key, req)
	if err != nil {
		h.handleError(w, err)
		return
	}
	if res.Replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(res.Code)
	_, _ = w.Write(res.Body)
}

// getOrder handles GET /v1/orders/{id}.
//
// It checks X-User-ID, parses the order ID from the path, and returns the
// order as JSON. A user can only fetch their own orders; another user's
// order is reported as 404.
func (h *Handler) getOrder(w http.ResponseWriter, r *http.Request) {
	userID, err := userFromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "missing or invalid X-User-ID")
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid order id")
		return
	}
	o, err := h.svc.GetOrder(r.Context(), userID, id)
	if err != nil {
		h.handleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

// handleError maps a service error to an HTTP status code:
//
//	ErrValidation -> 400 Bad Request
//	ErrKeyReuse   -> 422 Unprocessable Entity
//	ErrNotFound   -> 404 Not Found
//	anything else -> 500 Internal Server Error
//
// Unexpected errors are logged, and the client only sees "internal error"
// so internal details are not leaked.
func (h *Handler) handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrKeyReuse):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	default:
		h.log.Error("request failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes an error response in the form {"error": msg}.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
