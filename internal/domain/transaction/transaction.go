package transaction

import (
	"fmt"
	"time"

	"github.com/felipecristiano/desafio/internal/domain/errs"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/google/uuid"
)

type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

func (k Kind) IsExternal() bool { return k != KindOpening }

func (k Kind) IsValid() bool {
	switch k {
	case KindOpening, KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return true
	}
	return false
}

func (k Kind) RequiresReference() bool {
	return k == KindRefund || k == KindRollback
}

type Status string

const (
	StatusPending          Status = "PENDING"
	StatusPendingReference Status = "PENDING_REFERENCE"
	StatusProcessed        Status = "PROCESSED"
	StatusRejected         Status = "REJECTED"
	StatusFailed           Status = "FAILED"
)

func (s Status) IsTerminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}

type WagerTransaction struct {
	id                    uuid.UUID
	walletID              uuid.UUID
	playerID              uuid.UUID
	providerID            *string
	externalTransactionID *string
	idempotencyKey        *string
	payloadHash           *string
	roundID               *string
	gameID                *string

	kind   Kind
	status Status
	money  money.Money

	referenceExternalTransactionID *string
	referenceTransactionID         *uuid.UUID

	resultBalance *money.Money

	failureCode *string

	retryCount int
	retryAfter *time.Time

	correlationID *uuid.UUID

	createdAt time.Time
	updatedAt time.Time
}

func NewExternal(
	id uuid.UUID,
	walletID uuid.UUID,
	playerID uuid.UUID,
	providerID string,
	externalTransactionID string,
	idempotencyKey string,
	payloadHash string,
	roundID string,
	gameID string,
	kind Kind,
	m money.Money,
	referenceExternalTransactionID *string,
	correlationID *uuid.UUID,
) (*WagerTransaction, error) {
	if kind == KindOpening {
		return nil, errs.ErrOpeningForbidden
	}
	if !kind.IsValid() {
		return nil, errs.Newf(errs.CodeInvalidInput, "unknown transaction kind: %q", kind)
	}
	if id == uuid.Nil {
		return nil, errs.Newf(errs.CodeInvalidInput, "transaction ID is required")
	}
	if walletID == uuid.Nil {
		return nil, errs.Newf(errs.CodeInvalidInput, "wallet ID is required")
	}
	if playerID == uuid.Nil {
		return nil, errs.Newf(errs.CodeInvalidInput, "player ID is required")
	}
	if providerID == "" {
		return nil, errs.Newf(errs.CodeInvalidInput, "provider ID is required")
	}
	if externalTransactionID == "" {
		return nil, errs.Newf(errs.CodeInvalidInput, "external transaction ID is required")
	}
	if idempotencyKey == "" {
		return nil, errs.Newf(errs.CodeInvalidInput, "idempotency key is required")
	}
	if err := validateExternalMoney(kind, m); err != nil {
		return nil, err
	}
	if kind.RequiresReference() && (referenceExternalTransactionID == nil || *referenceExternalTransactionID == "") {
		return nil, errs.Newf(errs.CodeInvalidInput, "referenceExternalTransactionId is required for %s", kind)
	}

	now := time.Now().UTC()
	return &WagerTransaction{
		id:                             id,
		walletID:                       walletID,
		playerID:                       playerID,
		providerID:                     &providerID,
		externalTransactionID:          &externalTransactionID,
		idempotencyKey:                 &idempotencyKey,
		payloadHash:                    &payloadHash,
		roundID:                        &roundID,
		gameID:                         &gameID,
		kind:                           kind,
		status:                         StatusPending,
		money:                          m,
		referenceExternalTransactionID: referenceExternalTransactionID,
		correlationID:                  correlationID,
		createdAt:                      now,
		updatedAt:                      now,
	}, nil
}

func NewOpening(
	id uuid.UUID,
	walletID uuid.UUID,
	playerID uuid.UUID,
	m money.Money,
) (*WagerTransaction, error) {
	if id == uuid.Nil {
		return nil, errs.Newf(errs.CodeInvalidInput, "transaction ID is required")
	}
	if walletID == uuid.Nil {
		return nil, errs.Newf(errs.CodeInvalidInput, "wallet ID is required")
	}
	if playerID == uuid.Nil {
		return nil, errs.Newf(errs.CodeInvalidInput, "player ID is required")
	}
	if !m.IsPositive() {
		return nil, errs.Newf(errs.CodeInvalidInput, "OPENING requires positive initial balance")
	}

	now := time.Now().UTC()
	return &WagerTransaction{
		id:        id,
		walletID:  walletID,
		playerID:  playerID,
		kind:      KindOpening,
		status:    StatusPending,
		money:     m,
		createdAt: now,
		updatedAt: now,
	}, nil
}

func Rehydrate(
	id, walletID, playerID uuid.UUID,
	providerID, externalTransactionID, idempotencyKey, payloadHash *string,
	roundID, gameID *string,
	kind Kind,
	status Status,
	m money.Money,
	referenceExternalTransactionID *string,
	referenceTransactionID *uuid.UUID,
	resultBalance *money.Money,
	failureCode *string,
	retryCount int,
	retryAfter *time.Time,
	correlationID *uuid.UUID,
	createdAt, updatedAt time.Time,
) *WagerTransaction {
	return &WagerTransaction{
		id:                             id,
		walletID:                       walletID,
		playerID:                       playerID,
		providerID:                     providerID,
		externalTransactionID:          externalTransactionID,
		idempotencyKey:                 idempotencyKey,
		payloadHash:                    payloadHash,
		roundID:                        roundID,
		gameID:                         gameID,
		kind:                           kind,
		status:                         status,
		money:                          m,
		referenceExternalTransactionID: referenceExternalTransactionID,
		referenceTransactionID:         referenceTransactionID,
		resultBalance:                  resultBalance,
		failureCode:                    failureCode,
		retryCount:                     retryCount,
		retryAfter:                     retryAfter,
		correlationID:                  correlationID,
		createdAt:                      createdAt,
		updatedAt:                      updatedAt,
	}
}

func (t *WagerTransaction) MarkProcessed(resultBalance money.Money) error {
	if t.status.IsTerminal() {
		return errs.ErrTransactionTerminal
	}
	if t.status != StatusPending && t.status != StatusPendingReference {
		return errs.Newf(errs.CodeInvalidTransition,
			"cannot transition from %s to PROCESSED", t.status)
	}
	t.status = StatusProcessed
	t.resultBalance = &resultBalance
	t.updatedAt = time.Now().UTC()
	return nil
}

func (t *WagerTransaction) MarkRejected(failureCode string) error {
	if t.status.IsTerminal() {
		return errs.ErrTransactionTerminal
	}
	if t.status != StatusPending && t.status != StatusPendingReference {
		return errs.Newf(errs.CodeInvalidTransition,
			"cannot transition from %s to REJECTED", t.status)
	}
	t.status = StatusRejected
	t.failureCode = &failureCode
	t.updatedAt = time.Now().UTC()
	return nil
}

func (t *WagerTransaction) MarkFailed(failureCode string) error {
	if t.status.IsTerminal() {
		return errs.ErrTransactionTerminal
	}
	t.status = StatusFailed
	t.failureCode = &failureCode
	t.updatedAt = time.Now().UTC()
	return nil
}

func (t *WagerTransaction) MarkPendingReference(retryAfter time.Time) error {
	if t.status.IsTerminal() {
		return errs.ErrTransactionTerminal
	}
	if t.status != StatusPending {
		return errs.Newf(errs.CodeInvalidTransition,
			"cannot transition from %s to PENDING_REFERENCE", t.status)
	}
	t.status = StatusPendingReference
	t.retryCount++
	t.retryAfter = &retryAfter
	t.updatedAt = time.Now().UTC()
	return nil
}

func (t *WagerTransaction) IncrementRetry(retryAfter time.Time) error {
	if t.status != StatusPendingReference {
		return errs.Newf(errs.CodeInvalidTransition,
			"IncrementRetry only valid in PENDING_REFERENCE, got %s", t.status)
	}
	t.retryCount++
	t.retryAfter = &retryAfter
	t.updatedAt = time.Now().UTC()
	return nil
}

func (t *WagerTransaction) ResolveReference(refID uuid.UUID) {
	t.referenceTransactionID = &refID
}

func (t *WagerTransaction) ID() uuid.UUID        { return t.id }
func (t *WagerTransaction) WalletID() uuid.UUID  { return t.walletID }
func (t *WagerTransaction) PlayerID() uuid.UUID  { return t.playerID }
func (t *WagerTransaction) Kind() Kind           { return t.kind }
func (t *WagerTransaction) Status() Status       { return t.status }
func (t *WagerTransaction) Money() money.Money   { return t.money }
func (t *WagerTransaction) CreatedAt() time.Time { return t.createdAt }
func (t *WagerTransaction) UpdatedAt() time.Time { return t.updatedAt }
func (t *WagerTransaction) RetryCount() int      { return t.retryCount }
func (t *WagerTransaction) IsTerminal() bool     { return t.status.IsTerminal() }

func (t *WagerTransaction) ProviderID() *string            { return t.providerID }
func (t *WagerTransaction) ExternalTransactionID() *string { return t.externalTransactionID }
func (t *WagerTransaction) IdempotencyKey() *string        { return t.idempotencyKey }
func (t *WagerTransaction) PayloadHash() *string           { return t.payloadHash }
func (t *WagerTransaction) RoundID() *string               { return t.roundID }
func (t *WagerTransaction) GameID() *string                { return t.gameID }
func (t *WagerTransaction) ReferenceExternalTransactionID() *string {
	return t.referenceExternalTransactionID
}
func (t *WagerTransaction) ReferenceTransactionID() *uuid.UUID { return t.referenceTransactionID }
func (t *WagerTransaction) ResultBalance() *money.Money        { return t.resultBalance }
func (t *WagerTransaction) FailureCode() *string               { return t.failureCode }
func (t *WagerTransaction) RetryAfter() *time.Time             { return t.retryAfter }
func (t *WagerTransaction) CorrelationID() *uuid.UUID          { return t.correlationID }

func validateExternalMoney(kind Kind, m money.Money) error {
	switch kind {
	case KindLoss:
		if !m.IsZero() {
			return errs.Newf(errs.CodeInvalidMoney,
				"LOSS requires money.amount = \"0.00\", got %s", m)
		}
	case KindBet, KindWin, KindRefund, KindRollback:
		if !m.IsPositive() {
			return errs.Newf(errs.CodeInvalidAmount,
				"%s requires money.amount > 0, got %s", kind, m)
		}
	}
	return nil
}

func DirectionFor(kind Kind, referencedKind *Kind) (string, error) {
	switch kind {
	case KindBet:
		return "DEBIT", nil
	case KindWin, KindRefund:
		return "CREDIT", nil
	case KindLoss:
		return "", fmt.Errorf("LOSS does not produce a ledger entry")
	case KindRollback:
		if referencedKind == nil {
			return "", fmt.Errorf("ROLLBACK requires referenced kind to determine direction")
		}
		switch *referencedKind {
		case KindBet:
			return "CREDIT", nil // desfaz o débito da aposta → crédito
		case KindWin, KindRefund:
			return "DEBIT", nil // desfaz um crédito → débito
		default:
			return "", fmt.Errorf("ROLLBACK of %s is not supported", *referencedKind)
		}
	case KindOpening:
		return "CREDIT", nil
	}
	return "", fmt.Errorf("unknown kind: %s", kind)
}
