package events

import (
	"time"

	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/google/uuid"
)

const (
	TypeWagerTransactionProcessed        = "WagerTransactionProcessed"
	TypeWagerTransactionRejected         = "WagerTransactionRejected"
	TypeWalletBalanceChanged             = "WalletBalanceChanged"
	TypeWagerTransactionPendingReference = "WagerTransactionPendingReference"
)

const schemaVersion = 1

type Envelope struct {
	EventID       uuid.UUID   `json:"eventId"`
	EventType     string      `json:"eventType"`
	AggregateID   uuid.UUID   `json:"aggregateId"`
	AggregateType string      `json:"aggregateType"`
	CorrelationID uuid.UUID   `json:"correlationId"`
	CausationID   *uuid.UUID  `json:"causationId,omitempty"`
	OccurredAt    time.Time   `json:"occurredAt"`
	Version       int         `json:"version"`
}

type WagerTransactionProcessedData struct {
	TransactionID         uuid.UUID  `json:"transactionId"`
	WalletID              uuid.UUID  `json:"walletId"`
	PlayerID              uuid.UUID  `json:"playerId"`
	ProviderID            *string    `json:"providerId,omitempty"`
	ExternalTransactionID *string    `json:"externalTransactionId,omitempty"`
	Kind                  string     `json:"kind"`
	MoneyAmount           string     `json:"moneyAmount"`
	MoneyCurrency         string     `json:"moneyCurrency"`
	ResultBalanceAmount   string     `json:"resultBalanceAmount"`
	ResultBalanceCurrency string     `json:"resultBalanceCurrency"`
}

type WagerTransactionProcessed struct {
	Envelope
	Data WagerTransactionProcessedData `json:"data"`
}

func NewWagerTransactionProcessed(
	txnID, walletID, playerID uuid.UUID,
	providerID, externalTxnID *string,
	kind string,
	m money.Money,
	resultBalance money.Money,
	correlationID uuid.UUID,
	causationID *uuid.UUID,
) *WagerTransactionProcessed {
	return &WagerTransactionProcessed{
		Envelope: Envelope{
			EventID:       uuid.New(),
			EventType:     TypeWagerTransactionProcessed,
			AggregateID:   txnID,
			AggregateType: "WagerTransaction",
			CorrelationID: correlationID,
			CausationID:   causationID,
			OccurredAt:    time.Now().UTC(),
			Version:       schemaVersion,
		},
		Data: WagerTransactionProcessedData{
			TransactionID:         txnID,
			WalletID:              walletID,
			PlayerID:              playerID,
			ProviderID:            providerID,
			ExternalTransactionID: externalTxnID,
			Kind:                  kind,
			MoneyAmount:           m.String(),
			MoneyCurrency:         string(m.Currency()),
			ResultBalanceAmount:   resultBalance.String(),
			ResultBalanceCurrency: string(resultBalance.Currency()),
		},
	}
}


type WagerTransactionRejectedData struct {
	TransactionID         uuid.UUID `json:"transactionId"`
	WalletID              uuid.UUID `json:"walletId"`
	PlayerID              uuid.UUID `json:"playerId"`
	ProviderID            *string   `json:"providerId,omitempty"`
	ExternalTransactionID *string   `json:"externalTransactionId,omitempty"`
	Kind                  string    `json:"kind"`
	FailureCode           string    `json:"failureCode"`
}

type WagerTransactionRejected struct {
	Envelope
	Data WagerTransactionRejectedData `json:"data"`
}

func NewWagerTransactionRejected(
	txnID, walletID, playerID uuid.UUID,
	providerID, externalTxnID *string,
	kind string,
	failureCode string,
	correlationID uuid.UUID,
	causationID *uuid.UUID,
) *WagerTransactionRejected {
	return &WagerTransactionRejected{
		Envelope: Envelope{
			EventID:       uuid.New(),
			EventType:     TypeWagerTransactionRejected,
			AggregateID:   txnID,
			AggregateType: "WagerTransaction",
			CorrelationID: correlationID,
			CausationID:   causationID,
			OccurredAt:    time.Now().UTC(),
			Version:       schemaVersion,
		},
		Data: WagerTransactionRejectedData{
			TransactionID:         txnID,
			WalletID:              walletID,
			PlayerID:              playerID,
			ProviderID:            providerID,
			ExternalTransactionID: externalTxnID,
			Kind:                  kind,
			FailureCode:           failureCode,
		},
	}
}


type WalletBalanceChangedData struct {
	WalletID              uuid.UUID `json:"walletId"`
	TransactionID         uuid.UUID `json:"transactionId"`
	Direction             string    `json:"direction"`
	MoneyAmount           string    `json:"moneyAmount"`
	MoneyCurrency         string    `json:"moneyCurrency"`
	BalanceBeforeAmount   string    `json:"balanceBeforeAmount"`
	BalanceBeforeCurrency string    `json:"balanceBeforeCurrency"`
	BalanceAfterAmount    string    `json:"balanceAfterAmount"`
	BalanceAfterCurrency  string    `json:"balanceAfterCurrency"`
	WalletVersion         int64     `json:"walletVersion"`
}

type WalletBalanceChanged struct {
	Envelope
	Data WalletBalanceChangedData `json:"data"`
}

func NewWalletBalanceChanged(
	walletID, txnID uuid.UUID,
	direction string,
	m, balanceBefore, balanceAfter money.Money,
	walletVersion int64,
	correlationID uuid.UUID,
	causationID *uuid.UUID,
) *WalletBalanceChanged {
	return &WalletBalanceChanged{
		Envelope: Envelope{
			EventID:       uuid.New(),
			EventType:     TypeWalletBalanceChanged,
			AggregateID:   walletID,
			AggregateType: "Wallet",
			CorrelationID: correlationID,
			CausationID:   causationID,
			OccurredAt:    time.Now().UTC(),
			Version:       schemaVersion,
		},
		Data: WalletBalanceChangedData{
			WalletID:              walletID,
			TransactionID:         txnID,
			Direction:             direction,
			MoneyAmount:           m.String(),
			MoneyCurrency:         string(m.Currency()),
			BalanceBeforeAmount:   balanceBefore.String(),
			BalanceBeforeCurrency: string(balanceBefore.Currency()),
			BalanceAfterAmount:    balanceAfter.String(),
			BalanceAfterCurrency:  string(balanceAfter.Currency()),
			WalletVersion:         walletVersion,
		},
	}
}



type WagerTransactionPendingReferenceData struct {
	TransactionID                  uuid.UUID `json:"transactionId"`
	WalletID                       uuid.UUID `json:"walletId"`
	PlayerID                       uuid.UUID `json:"playerId"`
	ProviderID                     string    `json:"providerId"`
	ExternalTransactionID          string    `json:"externalTransactionId"`
	Kind                           string    `json:"kind"`
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId"`
	RetryCount                     int       `json:"retryCount"`
}

type WagerTransactionPendingReference struct {
	Envelope
	Data WagerTransactionPendingReferenceData `json:"data"`
}

func NewWagerTransactionPendingReference(
	txnID, walletID, playerID uuid.UUID,
	providerID, externalTxnID, kind, refExternalTxnID string,
	retryCount int,
	correlationID uuid.UUID,
	causationID *uuid.UUID,
) *WagerTransactionPendingReference {
	return &WagerTransactionPendingReference{
		Envelope: Envelope{
			EventID:       uuid.New(),
			EventType:     TypeWagerTransactionPendingReference,
			AggregateID:   txnID,
			AggregateType: "WagerTransaction",
			CorrelationID: correlationID,
			CausationID:   causationID,
			OccurredAt:    time.Now().UTC(),
			Version:       schemaVersion,
		},
		Data: WagerTransactionPendingReferenceData{
			TransactionID:                  txnID,
			WalletID:                       walletID,
			PlayerID:                       playerID,
			ProviderID:                     providerID,
			ExternalTransactionID:          externalTxnID,
			Kind:                           kind,
			ReferenceExternalTransactionID: refExternalTxnID,
			RetryCount:                     retryCount,
		},
	}
}
