package payment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/webhook"
)

type StripeConfig struct {
	SecretKey     string `json:"secret_key"`
	WebhookSecret string `json:"webhook_secret"`
}

type StripeMethodConfig struct{}

type StripeGateway struct {
	Config   StripeConfig
	Backends *stripe.Backends
}

type StripeReconciler struct {
	Config   StripeConfig
	Backends *stripe.Backends
}

func ValidateStripeConfig(cfg StripeConfig) error {
	if err := ValidateStripeSetupConfig(cfg); err != nil {
		return err
	}
	if strings.TrimSpace(cfg.WebhookSecret) == "" {
		return errors.New("Stripe webhook secret is required")
	}
	return nil
}

// ValidateStripeSetupConfig permits a disabled channel to be saved before its
// webhook endpoint exists. Any webhook secret that is present must still be
// structurally valid; enabled channels use ValidateStripeConfig above.
func ValidateStripeSetupConfig(cfg StripeConfig) error {
	key := strings.TrimSpace(cfg.SecretKey)
	if !strings.HasPrefix(key, "sk_") && !strings.HasPrefix(key, "rk_") && !strings.HasPrefix(key, "rkcs_") {
		return errors.New("invalid Stripe secret key")
	}
	webhookSecret := strings.TrimSpace(cfg.WebhookSecret)
	if webhookSecret != "" && !strings.HasPrefix(webhookSecret, "whsec_") {
		return errors.New("invalid Stripe webhook secret")
	}
	return nil
}

func (g StripeGateway) CreateCheckout(ctx context.Context, req CheckoutRequest) (CheckoutAction, error) {
	if err := ValidateStripeConfig(g.Config); err != nil {
		return CheckoutAction{}, err
	}
	key := strings.TrimSpace(g.Config.SecretKey)
	if req.AmountMinor <= 0 {
		return CheckoutAction{}, errors.New("payment amount must be positive")
	}
	currency := strings.ToLower(strings.TrimSpace(req.Currency))
	params := &stripe.CheckoutSessionCreateParams{
		ClientReferenceID:     stripe.String(req.OrderID),
		CustomerEmail:         stripe.String(req.UserEmail),
		SuccessURL:            stripe.String(req.SuccessURL),
		CancelURL:             stripe.String(req.CancelURL),
		Mode:                  stripe.String(stripe.CheckoutSessionModePayment),
		IntegrationIdentifier: stripe.String(stripeIntegrationIdentifier(req.OrderID)),
		PaymentIntentData:     &stripe.CheckoutSessionCreatePaymentIntentDataParams{},
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{{
			PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
				Currency:   stripe.String(currency),
				UnitAmount: stripe.Int64(req.AmountMinor),
				ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{
					Name: stripe.String(req.Name),
				},
			},
			Quantity: stripe.Int64(1),
		}},
	}
	params.AddMetadata("order_id", req.OrderID)
	params.PaymentIntentData.AddMetadata("order_id", req.OrderID)
	params.SetIdempotencyKey(req.OrderID)
	options := []stripe.ClientOption{}
	if g.Backends != nil {
		options = append(options, stripe.WithBackends(g.Backends))
	}
	api := stripe.NewClient(key, options...)
	session, err := api.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return CheckoutAction{}, fmt.Errorf("create Stripe Checkout session: %w", err)
	}
	if session == nil || !validRedirectURL(session.URL) {
		return CheckoutAction{}, errors.New("Stripe returned an invalid Checkout URL")
	}
	return CheckoutAction{
		Type: ActionRedirect, URL: session.URL, ProviderOrderID: session.ID,
		SessionID: session.ID, ExpiresAt: session.ExpiresAt,
	}, nil
}

// ResumeCheckout retrieves and returns the exact Checkout Session saved on the
// local order. It deliberately has no recovery/create fallback: an expired or
// otherwise inactive Session requires a separately recorded payment attempt.
func (g StripeGateway) ResumeCheckout(ctx context.Context, req CheckoutResumeRequest) (CheckoutAction, error) {
	if err := ValidateStripeConfig(g.Config); err != nil {
		return CheckoutAction{}, err
	}
	orderID := strings.TrimSpace(req.OrderID)
	sessionID := strings.TrimSpace(req.SessionID)
	if orderID == "" || sessionID == "" {
		return CheckoutAction{}, fmt.Errorf("%w: Stripe order and Checkout session references are required", ErrCheckoutNotResumable)
	}
	if providerOrderID := strings.TrimSpace(req.ProviderOrderID); providerOrderID != "" && providerOrderID != sessionID {
		return CheckoutAction{}, fmt.Errorf("%w: saved Stripe Checkout session references do not match", ErrCheckoutNotResumable)
	}

	options := []stripe.ClientOption{}
	if g.Backends != nil {
		options = append(options, stripe.WithBackends(g.Backends))
	}
	client := stripe.NewClient(strings.TrimSpace(g.Config.SecretKey), options...)
	session, err := client.V1CheckoutSessions.Retrieve(ctx, sessionID, nil)
	if err != nil {
		return CheckoutAction{}, fmt.Errorf("retrieve Stripe Checkout session for resume: %w", err)
	}
	if session == nil || strings.TrimSpace(session.ID) != sessionID {
		return CheckoutAction{}, fmt.Errorf("%w: Stripe returned a different Checkout session", ErrCheckoutNotResumable)
	}
	reference, err := stripeOrderReference(
		strings.TrimSpace(session.ClientReferenceID),
		stripeMetadataOrderID(session.Metadata),
		stripePaymentIntentMetadataOrderID(session.PaymentIntent),
	)
	if err != nil || reference != orderID {
		return CheckoutAction{}, fmt.Errorf("%w: Stripe Checkout session does not belong to the payment order", ErrCheckoutNotResumable)
	}
	if session.Mode != stripe.CheckoutSessionModePayment {
		return CheckoutAction{}, fmt.Errorf("%w: Stripe Checkout session mode is %s", ErrCheckoutNotResumable, session.Mode)
	}
	if session.Status == stripe.CheckoutSessionStatusExpired || (session.ExpiresAt > 0 && time.Now().Unix() >= session.ExpiresAt) {
		return CheckoutAction{}, fmt.Errorf("%w: Stripe Checkout session is no longer active", ErrCheckoutExpired)
	}
	if session.Status != stripe.CheckoutSessionStatusOpen || session.PaymentStatus != stripe.CheckoutSessionPaymentStatusUnpaid {
		return CheckoutAction{}, fmt.Errorf(
			"%w: Stripe Checkout session is %s/%s",
			ErrCheckoutNotResumable, session.Status, session.PaymentStatus,
		)
	}
	if session.ExpiresAt <= 0 || !validRedirectURL(session.URL) {
		return CheckoutAction{}, fmt.Errorf("%w: Stripe Checkout session has no active URL", ErrCheckoutNotResumable)
	}
	return CheckoutAction{
		Type: ActionRedirect, URL: session.URL, ResumeMode: CheckoutResumeOriginalSession,
		ProviderOrderID: session.ID, SessionID: session.ID, ExpiresAt: session.ExpiresAt,
	}, nil
}

func stripeIntegrationIdentifier(orderID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(orderID)))
	suffix := make([]byte, 8)
	for index := range suffix {
		suffix[index] = 'a' + digest[index]%26
	}
	return "aivory_" + string(suffix)
}

func (r StripeReconciler) Reconcile(ctx context.Context, req ReconcileRequest) (ProviderEvent, error) {
	if err := ValidateStripeConfig(r.Config); err != nil {
		return ProviderEvent{}, err
	}
	orderID := strings.TrimSpace(req.OrderID)
	sessionID := strings.TrimSpace(req.SessionID)
	if orderID == "" {
		return ProviderEvent{}, fmt.Errorf("%w: Stripe payment order reference is unavailable", ErrCheckoutStateUnknown)
	}
	options := []stripe.ClientOption{}
	if r.Backends != nil {
		options = append(options, stripe.WithBackends(r.Backends))
	}
	client := stripe.NewClient(strings.TrimSpace(r.Config.SecretKey), options...)
	session, err := retrieveStripeReconciliationSession(ctx, client, orderID, sessionID)
	if err != nil {
		return ProviderEvent{}, err
	}
	sessionID = strings.TrimSpace(session.ID)
	if req.Close && session.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
		if session.Status != stripe.CheckoutSessionStatusOpen {
			if session.Status != stripe.CheckoutSessionStatusExpired {
				return ProviderEvent{}, fmt.Errorf("%w: Stripe Checkout session status is %s", ErrCheckoutNotClosable, session.Status)
			}
		} else {
			session, err = client.V1CheckoutSessions.Expire(ctx, sessionID, nil)
			if err != nil {
				return ProviderEvent{}, fmt.Errorf("expire Stripe Checkout session: %w", err)
			}
		}
	}
	return stripeReconcileEvent(orderID, session)
}

// retrieveStripeReconciliationSession first uses the immutable Checkout
// Session snapshot. When checkout creation timed out after Stripe accepted the
// request, the local order has no session ID; in that case search PaymentIntents
// by the order metadata written at creation time, then list Checkout Sessions
// for the matching PaymentIntent. Search is deliberately bounded and treated
// as state-unknown when Stripe's eventually-consistent index has not caught up.
func retrieveStripeReconciliationSession(ctx context.Context, client *stripe.Client, orderID, sessionID string) (*stripe.CheckoutSession, error) {
	if sessionID != "" {
		session, err := client.V1CheckoutSessions.Retrieve(ctx, sessionID, nil)
		if err != nil {
			return nil, fmt.Errorf("retrieve Stripe Checkout session: %w", err)
		}
		return session, nil
	}
	if strings.ContainsAny(orderID, "'\\") {
		return nil, fmt.Errorf("%w: invalid Stripe order reference", ErrCheckoutStateUnknown)
	}
	searchParams := &stripe.PaymentIntentSearchParams{
		SearchParams: stripe.SearchParams{
			Query:  "metadata['order_id']:'" + orderID + "'",
			Limit:  stripe.Int64(10),
			Single: true,
		},
	}
	intents := client.V1PaymentIntents.Search(ctx, searchParams)
	if err := intents.Err(); err != nil {
		return nil, fmt.Errorf("search Stripe PaymentIntent for payment order: %w", err)
	}
	for _, intent := range intents.Data() {
		if intent == nil || strings.TrimSpace(intent.ID) == "" || strings.TrimSpace(intent.Metadata["order_id"]) != orderID {
			continue
		}
		listParams := &stripe.CheckoutSessionListParams{
			ListParams:    stripe.ListParams{Limit: stripe.Int64(10), Single: true},
			PaymentIntent: stripe.String(intent.ID),
		}
		sessions := client.V1CheckoutSessions.List(ctx, listParams)
		if err := sessions.Err(); err != nil {
			return nil, fmt.Errorf("list Stripe Checkout sessions for payment order: %w", err)
		}
		for _, session := range sessions.Data() {
			if session == nil || strings.TrimSpace(session.ID) == "" {
				continue
			}
			return session, nil
		}
	}
	return nil, fmt.Errorf("%w: Stripe Checkout session ID is unavailable", ErrCheckoutStateUnknown)
}

func stripeReconcileEvent(orderID string, session *stripe.CheckoutSession) (ProviderEvent, error) {
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return ProviderEvent{}, errors.New("Stripe returned an invalid Checkout session")
	}
	reference, err := stripeOrderReference(
		strings.TrimSpace(session.ClientReferenceID),
		stripeMetadataOrderID(session.Metadata),
		stripePaymentIntentMetadataOrderID(session.PaymentIntent),
	)
	if err != nil {
		return ProviderEvent{}, err
	}
	if reference != orderID {
		return ProviderEvent{}, errors.New("Stripe Checkout session does not belong to the payment order")
	}
	if session.Mode != stripe.CheckoutSessionModePayment {
		return ProviderEvent{}, errors.New("Stripe Checkout session mode is not payment")
	}
	status := EventProcessing
	if session.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid {
		status = EventPaid
	} else if session.Status == stripe.CheckoutSessionStatusExpired {
		status = EventExpired
	}
	eventID := "reconcile:" + session.ID + ":" + status
	return ProviderEvent{
		ID: eventID, Type: "checkout.session.reconciled", Status: status,
		OrderID: orderID, ProviderOrderID: session.ID, ProviderPaymentID: stripePaymentIntentID(session.PaymentIntent),
		AmountMinor: session.AmountTotal, PaidAmountMinor: session.AmountTotal,
		Currency: strings.ToUpper(string(session.Currency)),
	}, nil
}

func VerifyStripeEvent(payload []byte, signature string, cfg StripeConfig) (ProviderEvent, error) {
	secret := strings.TrimSpace(cfg.WebhookSecret)
	if secret == "" {
		return ProviderEvent{}, errors.New("Stripe webhook secret is not configured")
	}
	event, err := webhook.ConstructEventWithOptions(payload, signature, secret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		return ProviderEvent{}, fmt.Errorf("invalid Stripe webhook signature: %w", err)
	}
	providerEvent := ProviderEvent{ID: event.ID, Type: string(event.Type), Status: EventIgnored}
	switch event.Type {
	case stripe.EventTypeCheckoutSessionCompleted,
		stripe.EventTypeCheckoutSessionExpired,
		stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded,
		stripe.EventTypeCheckoutSessionAsyncPaymentFailed:
		return stripeCheckoutProviderEvent(event, providerEvent)
	default:
		return providerEvent, nil
	}
}

func stripeCheckoutProviderEvent(event stripe.Event, providerEvent ProviderEvent) (ProviderEvent, error) {
	var session stripe.CheckoutSession
	if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
		return ProviderEvent{}, fmt.Errorf("decode Stripe Checkout session: %w", err)
	}
	orderID, err := stripeOrderReference(
		strings.TrimSpace(session.ClientReferenceID),
		stripeMetadataOrderID(session.Metadata),
		stripePaymentIntentMetadataOrderID(session.PaymentIntent),
	)
	if err != nil {
		return ProviderEvent{}, err
	}
	if orderID == "" {
		return ProviderEvent{}, errors.New("Stripe Checkout session is missing the order reference")
	}
	if session.Mode != stripe.CheckoutSessionModePayment {
		return ProviderEvent{}, errors.New("Stripe Checkout session mode is not payment")
	}
	providerEvent.OrderID = orderID
	providerEvent.ProviderOrderID = session.ID
	providerEvent.ProviderPaymentID = stripePaymentIntentID(session.PaymentIntent)
	providerEvent.AmountMinor = session.AmountTotal
	providerEvent.PaidAmountMinor = session.AmountTotal
	providerEvent.Currency = strings.ToUpper(string(session.Currency))
	switch event.Type {
	case stripe.EventTypeCheckoutSessionCompleted:
		if session.Status != stripe.CheckoutSessionStatusComplete {
			return ProviderEvent{}, errors.New("Stripe Checkout session is not complete")
		}
		if session.PaymentStatus == stripe.CheckoutSessionPaymentStatusPaid {
			providerEvent.Status = EventPaid
		} else {
			providerEvent.Status = EventProcessing
		}
	case stripe.EventTypeCheckoutSessionAsyncPaymentSucceeded:
		if session.PaymentStatus != stripe.CheckoutSessionPaymentStatusPaid {
			return ProviderEvent{}, errors.New("Stripe async success session is not paid")
		}
		providerEvent.Status = EventPaid
	case stripe.EventTypeCheckoutSessionAsyncPaymentFailed:
		providerEvent.Status = EventFailed
		providerEvent.FailureReason = "Stripe asynchronous payment failed"
	case stripe.EventTypeCheckoutSessionExpired:
		providerEvent.Status = EventExpired
	}
	return providerEvent, nil
}
func stripeOrderReference(references ...string) (string, error) {
	reference := ""
	for _, candidate := range references {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if reference != "" && reference != candidate {
			return "", errors.New("Stripe payment order references do not match")
		}
		reference = candidate
	}
	return reference, nil
}

func stripeMetadataOrderID(metadata map[string]string) string {
	return strings.TrimSpace(metadata["order_id"])
}

func stripePaymentIntentID(intent *stripe.PaymentIntent) string {
	if intent == nil {
		return ""
	}
	return strings.TrimSpace(intent.ID)
}

func stripePaymentIntentMetadataOrderID(intent *stripe.PaymentIntent) string {
	if intent == nil {
		return ""
	}
	return stripeMetadataOrderID(intent.Metadata)
}

func validRedirectURL(raw string) bool {
	u, err := validateGatewayURL(raw)
	return err == nil && u.String() != ""
}
