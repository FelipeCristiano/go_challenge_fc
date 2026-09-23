package errs

import "fmt"

type DomainError struct {
	Code    string
	Message string
}

func (e *DomainError) Error() string {
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

func (e *DomainError) Is(target error) bool {
	t, ok := target.(*DomainError)
	if !ok {
		return false
	}
	return e.Code == t.Code
}

func New(code, message string) *DomainError {
	return &DomainError{Code: code, Message: message}
}

func Newf(code, format string, args ...any) *DomainError {
	return &DomainError{Code: code, Message: fmt.Sprintf(format, args...)}
}

const (
	CodeInsufficientFunds          = "INSUFFICIENT_FUNDS"
	CodeRollbackInsufficientFunds  = "ROLLBACK_INSUFFICIENT_FUNDS"
	CodeCurrencyMismatch           = "CURRENCY_MISMATCH"
	CodeInvalidMoney               = "INVALID_MONEY"
	CodeInvalidAmount              = "INVALID_AMOUNT"
	CodeNegativeBalance            = "NEGATIVE_BALANCE"
	CodeTransactionTerminal        = "TRANSACTION_TERMINAL"
	CodeInvalidTransition          = "INVALID_TRANSITION"
	CodeWalletAlreadyExists        = "WALLET_ALREADY_EXISTS"
	CodeWalletNotFound             = "WALLET_NOT_FOUND"
	CodeReferenceAlreadyReversed   = "REFERENCE_ALREADY_REVERSED"
	CodeReferenceNotFound          = "REFERENCE_NOT_FOUND"
	CodeReferenceNotProcessable    = "REFERENCE_NOT_PROCESSABLE"
	CodeOpeningForbidden           = "OPENING_FORBIDDEN"
	CodeDuplicateOperation         = "DUPLICATE_OPERATION"
	CodePayloadConflict            = "PAYLOAD_CONFLICT"
	CodeInvalidInput               = "INVALID_INPUT"
	CodeTransactionNotFound        = "TRANSACTION_NOT_FOUND"
	CodeInboxDuplicate             = "INBOX_DUPLICATE"
)

var (
	ErrInsufficientFunds         = New(CodeInsufficientFunds, "insufficient wallet balance for BET")
	ErrRollbackInsufficientFunds = New(CodeRollbackInsufficientFunds, "insufficient wallet balance for ROLLBACK")
	ErrCurrencyMismatch          = New(CodeCurrencyMismatch, "operation currency does not match wallet currency")
	ErrInvalidMoney              = New(CodeInvalidMoney, "invalid monetary value")
	ErrInvalidAmount             = New(CodeInvalidAmount, "amount must be positive")
	ErrNegativeBalance           = New(CodeNegativeBalance, "wallet balance cannot be negative")
	ErrTransactionTerminal       = New(CodeTransactionTerminal, "transaction is in a terminal state and cannot be transitioned")
	ErrInvalidTransition         = New(CodeInvalidTransition, "invalid state transition")
	ErrWalletAlreadyExists       = New(CodeWalletAlreadyExists, "wallet already exists for this player and currency")
	ErrWalletNotFound            = New(CodeWalletNotFound, "wallet not found")
	ErrReferenceAlreadyReversed  = New(CodeReferenceAlreadyReversed, "reference transaction has already been reversed")
	ErrReferenceNotFound         = New(CodeReferenceNotFound, "reference transaction not found after maximum retries")
	ErrReferenceNotProcessable   = New(CodeReferenceNotProcessable, "reference transaction exists but is not in a processable state")
	ErrOpeningForbidden          = New(CodeOpeningForbidden, "OPENING transactions cannot be submitted via external API or SQS")
	ErrDuplicateOperation        = New(CodeDuplicateOperation, "operation already exists with the same idempotency key")
	ErrPayloadConflict           = New(CodePayloadConflict, "idempotency key reused with a different payload")
	ErrInvalidInput              = New(CodeInvalidInput, "invalid input")
	ErrTransactionNotFound       = New(CodeTransactionNotFound, "transaction not found")
	ErrInboxDuplicate            = New(CodeInboxDuplicate, "message already processed by this consumer")
)
