package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/application/usecase"
	"github.com/felipecristiano/desafio/internal/domain/errs"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/http/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type WalletHandler struct {
	openWalletUC *usecase.OpenWalletUseCase
	reconcileUC  *usecase.ReconcileWalletUseCase
	walletRepo   port.WalletRepository
	ledgerRepo   port.LedgerRepository
	uow          port.UnitOfWork
}

func NewWalletHandler(
	openWalletUC *usecase.OpenWalletUseCase,
	reconcileUC *usecase.ReconcileWalletUseCase,
	walletRepo port.WalletRepository,
	ledgerRepo port.LedgerRepository,
	uow port.UnitOfWork,
) *WalletHandler {
	return &WalletHandler{
		openWalletUC: openWalletUC,
		reconcileUC:  reconcileUC,
		walletRepo:   walletRepo,
		ledgerRepo:   ledgerRepo,
		uow:          uow,
	}
}

type MoneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type OpenWalletRequest struct {
	PlayerID       string   `json:"playerId"`
	InitialBalance MoneyDTO `json:"initialBalance"`
}

type WalletResponse struct {
	ID       string   `json:"id"`
	PlayerID string   `json:"playerId"`
	Balance  MoneyDTO `json:"balance"`
	Version  int64    `json:"version"`
}

func (h *WalletHandler) OpenWallet(w http.ResponseWriter, r *http.Request) {
	var req OpenWalletRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json", "message": err.Error()})
		return
	}

	playerID, err := uuid.Parse(req.PlayerID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_input", "message": "invalid playerId"})
		return
	}

	initBal, err := money.NewFromExternalString(req.InitialBalance.Amount, money.Currency(req.InitialBalance.Currency))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_money", "message": err.Error()})
		return
	}

	corrID := middleware.GetCorrelationID(r.Context())

	out, err := h.openWalletUC.Execute(r.Context(), usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
		CorrelationID:  corrID,
	})
	if err != nil {
		if errors.Is(err, errs.ErrWalletAlreadyExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "wallet_already_exists", "message": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "open_wallet_failed", "message": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, WalletResponse{
		ID:       out.ID.String(),
		PlayerID: out.PlayerID.String(),
		Balance: MoneyDTO{
			Amount:   out.Balance.String(),
			Currency: string(out.Balance.Currency()),
		},
		Version: out.Version,
	})
}

func (h *WalletHandler) GetWallet(w http.ResponseWriter, r *http.Request) {
	walletIDStr := chi.URLParam(r, "walletId")
	walletID, err := uuid.Parse(walletIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_wallet_id"})
		return
	}

	var foundWallet *WalletResponse
	err = h.uow.WithTx(r.Context(), func(tx port.DBTX) error {
		entity, err := h.walletRepo.GetByID(r.Context(), tx, walletID)
		if err != nil {
			return err
		}
		foundWallet = &WalletResponse{
			ID:       entity.ID().String(),
			PlayerID: entity.PlayerID().String(),
			Balance: MoneyDTO{
				Amount:   entity.Balance().String(),
				Currency: string(entity.Balance().Currency()),
			},
			Version: entity.Version(),
		}
		return nil
	})

	if err != nil {
		if errors.Is(err, errs.ErrWalletNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "wallet_not_found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error", "message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, foundWallet)
}

type LedgerEntryDTO struct {
	ID            string   `json:"id"`
	WalletID      string   `json:"walletId"`
	TransactionID string   `json:"transactionId"`
	Direction     string   `json:"direction"`
	Money         MoneyDTO `json:"money"`
	BalanceBefore MoneyDTO `json:"balanceBefore"`
	BalanceAfter  MoneyDTO `json:"balanceAfter"`
	CreatedAt     string   `json:"createdAt"`
}

type LedgerListResponse struct {
	Entries    []LedgerEntryDTO `json:"entries"`
	NextCursor *string          `json:"nextCursor,omitempty"`
}

func (h *WalletHandler) GetLedger(w http.ResponseWriter, r *http.Request) {
	walletIDStr := chi.URLParam(r, "walletId")
	walletID, err := uuid.Parse(walletIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_wallet_id"})
		return
	}

	limit := 50
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
			limit = l
		}
	}

	var cursor *uuid.UUID
	if cursorStr := r.URL.Query().Get("cursor"); cursorStr != "" {
		if c, err := uuid.Parse(cursorStr); err == nil {
			cursor = &c
		}
	}

	var resp LedgerListResponse
	err = h.uow.WithTx(r.Context(), func(tx port.DBTX) error {
		entries, err := h.ledgerRepo.GetByWalletID(r.Context(), tx, walletID, cursor, limit)
		if err != nil {
			return err
		}

		dtos := make([]LedgerEntryDTO, 0, len(entries))
		for _, e := range entries {
			dtos = append(dtos, LedgerEntryDTO{
				ID:            e.ID().String(),
				WalletID:      e.WalletID().String(),
				TransactionID: e.TransactionID().String(),
				Direction:     string(e.Direction()),
				Money: MoneyDTO{
					Amount:   e.Money().String(),
					Currency: string(e.Money().Currency()),
				},
				BalanceBefore: MoneyDTO{
					Amount:   e.BalanceBefore().String(),
					Currency: string(e.BalanceBefore().Currency()),
				},
				BalanceAfter: MoneyDTO{
					Amount:   e.BalanceAfter().String(),
					Currency: string(e.BalanceAfter().Currency()),
				},
				CreatedAt: e.CreatedAt().Format("2006-01-02T15:04:05.000Z"),
			})
		}
		resp.Entries = dtos
		if len(entries) == limit {
			lastID := entries[len(entries)-1].ID().String()
			resp.NextCursor = &lastID
		}
		return nil
	})

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal_error", "message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *WalletHandler) Reconcile(w http.ResponseWriter, r *http.Request) {
	walletIDStr := chi.URLParam(r, "walletId")
	walletID, err := uuid.Parse(walletIDStr)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_wallet_id"})
		return
	}

	out, err := h.reconcileUC.Execute(r.Context(), usecase.ReconcileWalletInput{
		WalletID: walletID,
	})
	if err != nil {
		if errors.Is(err, errs.ErrWalletNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "wallet_not_found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reconciliation_failed", "message": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"walletId": out.WalletID.String(),
		"storedBalance": MoneyDTO{
			Amount:   out.StoredBalance.String(),
			Currency: string(out.StoredBalance.Currency()),
		},
		"calculatedBalance": MoneyDTO{
			Amount:   out.CalculatedBalance.String(),
			Currency: string(out.CalculatedBalance.Currency()),
		},
		"difference": MoneyDTO{
			Amount:   out.Difference.String(),
			Currency: string(out.Difference.Currency()),
		},
		"consistent":     out.Consistent,
		"checkedEntries": out.CheckedEntries,
	})
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
