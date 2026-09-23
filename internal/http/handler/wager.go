package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/application/usecase"
	"github.com/felipecristiano/desafio/internal/domain/errs"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/transaction"
	"github.com/felipecristiano/desafio/internal/http/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type WagerHandler struct {
	processWagerUC *usecase.ProcessWagerUseCase
	txnRepo        port.WagerTransactionRepository
	uow            port.UnitOfWork
}

func NewWagerHandler(
	processWagerUC *usecase.ProcessWagerUseCase,
	txnRepo port.WagerTransactionRepository,
	uow port.UnitOfWork,
) *WagerHandler {
	return &WagerHandler{
		processWagerUC: processWagerUC,
		txnRepo:        txnRepo,
		uow:            uow,
	}
}

type WagerRequest struct {
	ProviderID                     string   `json:"providerId"`
	ExternalTransactionID          string   `json:"externalTransactionId"`
	PlayerID                       string   `json:"playerId"`
	WalletID                       string   `json:"walletId"`
	RoundID                        string   `json:"roundId"`
	GameID                         string   `json:"gameId"`
	Kind                           string   `json:"kind"`
	Money                          MoneyDTO `json:"money"`
	ReferenceExternalTransactionID *string  `json:"referenceExternalTransactionId,omitempty"`
}

type WagerResponse struct {
	TransactionID    string   `json:"transactionId"`
	Status           string   `json:"status"`
	Balance          MoneyDTO `json:"balance"`
	IdempotentReplay bool     `json:"idempotentReplay"`
	FailureCode      *string  `json:"failureCode,omitempty"`
}

func (h *WagerHandler) ProcessWager(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "missing_header",
			"message": "Idempotency-Key header is required",
		})
		return
	}

	claims, ok := middleware.GetClaims(r.Context())
	if !ok || claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req WagerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}

	// Isolamento de Tenant: O token autenticado determina o provider autorizado
	if claims.ClientID != "" && claims.ClientID != req.ProviderID && !claims.HasRole("internal") {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":   "forbidden",
			"message": "authenticated identity does not match requested providerId",
		})
		return
	}

	playerID, err := uuid.Parse(req.PlayerID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_input", "message": "invalid playerId"})
		return
	}

	walletID, err := uuid.Parse(req.WalletID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_input", "message": "invalid walletId"})
		return
	}

	m, err := money.NewFromExternalString(req.Money.Amount, money.Currency(req.Money.Currency))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_money", "message": err.Error()})
		return
	}

	corrID := middleware.GetCorrelationID(r.Context())

	out, err := h.processWagerUC.Execute(r.Context(), usecase.ProcessWagerInput{
		Source:                         "HTTP",
		IdempotencyKey:                 idempotencyKey,
		CorrelationID:                  corrID,
		ProviderID:                     req.ProviderID,
		ExternalTransactionID:          req.ExternalTransactionID,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        req.RoundID,
		GameID:                         req.GameID,
		Kind:                           transaction.Kind(req.Kind),
		Money:                          m,
		ReferenceExternalTransactionID: req.ReferenceExternalTransactionID,
	})

	if err != nil {
		if errors.Is(err, errs.ErrPayloadConflict) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":   "payload_conflict",
				"message": err.Error(),
			})
			return
		}
		if errors.Is(err, errs.ErrOpeningForbidden) {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error":   "opening_forbidden",
				"message": err.Error(),
			})
			return
		}
		if errors.Is(err, errs.ErrWalletNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":   "wallet_not_found",
				"message": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "invalid_request",
			"message": err.Error(),
		})
		return
	}

	statusHTTP := http.StatusOK
	if out.Status == transaction.StatusPendingReference {
		statusHTTP = http.StatusAccepted
	}

	writeJSON(w, statusHTTP, WagerResponse{
		TransactionID:    out.TransactionID.String(),
		Status:           string(out.Status),
		Balance:          MoneyDTO{Amount: out.Balance.String(), Currency: string(out.Balance.Currency())},
		IdempotentReplay: out.IdempotentReplay,
		FailureCode:      out.FailureCode,
	})
}

func (h *WagerHandler) GetTransactionByID(w http.ResponseWriter, r *http.Request) {
	txnIDStr := chi.URLParam(r, "transactionId")
	txnID, err := uuid.Parse(txnIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_transaction_id"})
		return
	}

	claims, _ := middleware.GetClaims(r.Context())

	var txnResp *WagerResponse
	err = h.uow.WithTx(r.Context(), func(tx port.DBTX) error {
		t, err := h.txnRepo.GetByID(r.Context(), tx, txnID)
		if err != nil {
			return err
		}

		// Isolamento de tenant: apenas o próprio provedor ou papel internal
		if claims != nil && claims.ClientID != "" && t.ProviderID() != nil && *t.ProviderID() != claims.ClientID && !claims.HasRole("internal") {
			return errs.New("forbidden", "unauthorized provider access")
		}

		resBal := t.Money()
		if t.ResultBalance() != nil {
			resBal = *t.ResultBalance()
		}

		txnResp = &WagerResponse{
			TransactionID:    t.ID().String(),
			Status:           string(t.Status()),
			Balance:          MoneyDTO{Amount: resBal.String(), Currency: string(resBal.Currency())},
			IdempotentReplay: false,
			FailureCode:      t.FailureCode(),
		}
		return nil
	})

	if err != nil {
		if errors.Is(err, errs.ErrTransactionNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "transaction_not_found"})
			return
		}
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, txnResp)
}

func (h *WagerHandler) GetTransactionByProviderAndExternalID(w http.ResponseWriter, r *http.Request) {
	providerID := chi.URLParam(r, "providerId")
	extID := chi.URLParam(r, "externalTransactionId")

	claims, _ := middleware.GetClaims(r.Context())
	if claims != nil && claims.ClientID != "" && providerID != claims.ClientID && !claims.HasRole("internal") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden", "message": "cannot view transactions of another provider"})
		return
	}

	var txnResp *WagerResponse
	err := h.uow.WithTx(r.Context(), func(tx port.DBTX) error {
		t, err := h.txnRepo.GetByProviderAndExternalID(r.Context(), tx, providerID, extID)
		if err != nil {
			return err
		}

		resBal := t.Money()
		if t.ResultBalance() != nil {
			resBal = *t.ResultBalance()
		}

		txnResp = &WagerResponse{
			TransactionID:    t.ID().String(),
			Status:           string(t.Status()),
			Balance:          MoneyDTO{Amount: resBal.String(), Currency: string(resBal.Currency())},
			IdempotentReplay: false,
			FailureCode:      t.FailureCode(),
		}
		return nil
	})

	if err != nil {
		if errors.Is(err, errs.ErrTransactionNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "transaction_not_found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error", "message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, txnResp)
}
