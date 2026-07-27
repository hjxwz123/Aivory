package payment

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"syscall"
)

var ErrCheckoutStateUnknown = errors.New("payment provider checkout state is unknown")
var ErrCheckoutNotClosable = errors.New("payment provider checkout cannot be closed safely")
var ErrReconciliationUnsupported = errors.New("payment provider reconciliation is unsupported")

// IsCheckoutStateUnknown classifies transport failures where the provider may
// have accepted the request even though Aivory did not receive a response.
// Such orders must remain reconcilable instead of being marked definitively
// failed and allowing a second charge attempt immediately.
func IsCheckoutStateUnknown(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrCheckoutStateUnknown) || errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

const (
	ProviderStripe = "stripe"
	ProviderEPay   = "epay"
	ProviderWaffo  = "waffo"
)

const (
	ActionRedirect = "redirect"
	ActionFormPost = "form_post"
)

const (
	EventPaid       = "paid"
	EventProcessing = "processing"
	EventFailed     = "failed"
	EventExpired    = "expired"
	EventIgnored    = "ignored"
)

type ReconcileRequest struct {
	OrderID          string
	UserID           string
	AmountMinor      int64
	Currency         string
	SessionID        string
	SessionExpiresAt int64
	Close            bool
}

type CheckoutRequest struct {
	OrderID     string
	Name        string
	AmountMinor int64
	Currency    string
	TaxCategory string
	UserID      string
	UserEmail   string
	NotifyURL   string
	SuccessURL  string
	CancelURL   string
}

type CheckoutAction struct {
	Type            string            `json:"type"`
	URL             string            `json:"url"`
	Fields          map[string]string `json:"fields,omitempty"`
	ProviderOrderID string            `json:"-"`
	SessionID       string            `json:"-"`
	ExpiresAt       int64             `json:"-"`
}

type ProviderEvent struct {
	ID                string
	Type              string
	Status            string
	OrderID           string
	ProviderOrderID   string
	ProviderPaymentID string
	// AmountMinor is the tax-exclusive catalog amount used to authorize the
	// snapshotted local order. PaidAmountMinor is the actual provider charge;
	// providers without added tax set both to the same value.
	AmountMinor     int64
	PaidAmountMinor int64
	TaxAmountMinor  int64
	Currency        string
	MethodType      string
	MethodName      string
	UserID          string
	FailureReason   string
}

const (
	TaxCategoryDigitalGoods = "digital_goods"
	TaxCategorySaaS         = "saas"
)

type CheckoutCreator interface {
	CreateCheckout(context.Context, CheckoutRequest) (CheckoutAction, error)
}
