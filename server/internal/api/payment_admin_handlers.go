package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	paymentcore "aivory/server/internal/payment"
	"aivory/server/internal/store"
)

type paymentChannelPayload struct {
	ID          *string          `json:"id"`
	Name        *string          `json:"name"`
	Provider    *string          `json:"provider"`
	Environment *string          `json:"environment"`
	Config      *json.RawMessage `json:"config"`
	Enabled     *bool            `json:"enabled"`
	SortOrder   *int             `json:"sort_order"`
}

type adminPaymentChannelResponse struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Provider    string          `json:"provider"`
	Environment string          `json:"environment"`
	Config      json.RawMessage `json:"config"`
	Enabled     bool            `json:"enabled"`
	SortOrder   int             `json:"sort_order"`
	WebhookURL  string          `json:"webhook_url"`
	CreatedAt   int64           `json:"created_at"`
	UpdatedAt   int64           `json:"updated_at"`
}

type preparedPaymentChannelResponse struct {
	ID         string `json:"id"`
	WebhookURL string `json:"webhook_url"`
}

func paymentWebhookURL(r *http.Request, channelID string) string {
	return strings.TrimRight(externalBaseURL(r), "/") + "/api/payments/webhooks/" + url.PathEscape(channelID)
}

func adminPaymentChannelJSON(channel store.PaymentChannel, r *http.Request) adminPaymentChannelResponse {
	return adminPaymentChannelResponse{
		ID: channel.ID, Name: channel.Name, Provider: channel.Provider, Environment: channel.Environment,
		Config:  maskedPaymentChannelConfig(channel.Provider, channel.Config),
		Enabled: channel.Enabled, SortOrder: channel.SortOrder,
		WebhookURL: paymentWebhookURL(r, channel.ID),
		CreatedAt:  channel.CreatedAt, UpdatedAt: channel.UpdatedAt,
	}
}

func writePaymentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound)
	case errors.Is(err, store.ErrPaymentChannelNameExists), errors.Is(err, store.ErrPaymentChannelIDExists),
		errors.Is(err, store.ErrPaymentMethodNameExists), errors.Is(err, store.ErrPaymentMethodHasPending),
		errors.Is(err, store.ErrPaymentChannelHasMethods), errors.Is(err, store.ErrPaymentChannelHasPending),
		errors.Is(err, store.ErrPaymentOrdersPendingForGroup), errors.Is(err, store.ErrPaymentOrdersPendingForUser),
		errors.Is(err, store.ErrPaymentProviderOrderConflict), errors.Is(err, store.ErrPaymentOrderNotMutable),
		errors.Is(err, store.ErrPaymentOrderNotDeletable), errors.Is(err, store.ErrPaymentOrderDeleteNeedsAck):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, store.ErrInvalidPaymentChannel), errors.Is(err, store.ErrInvalidPaymentMethod),
		errors.Is(err, store.ErrInvalidPaymentProduct), errors.Is(err, store.ErrPaymentMethodUnavailable),
		errors.Is(err, store.ErrPaymentProductUnavailable):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func listPaymentChannelsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	channels, err := store.ListPaymentChannels(r.Context(), d.DB)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	response := make([]adminPaymentChannelResponse, 0, len(channels))
	for _, channel := range channels {
		response = append(response, adminPaymentChannelJSON(channel, r))
	}
	writeJSON(w, http.StatusOK, response)
}

func reorderPaymentChannelsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	channels, err := store.ListPaymentChannels(r.Context(), d.DB)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	if len(body.IDs) != len(channels) {
		writeError(w, http.StatusBadRequest, errors.New("payment channel reorder list must include every channel"))
		return
	}
	valid := make(map[string]bool, len(channels))
	for _, channel := range channels {
		valid[channel.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range body.IDs {
		if !valid[id] || seen[id] {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
		seen[id] = true
	}
	if err := store.ReorderPaymentChannels(r.Context(), d.DB, body.IDs); err != nil {
		writePaymentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func preparePaymentChannelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	for range 4 {
		id := store.GenID("paych")
		if _, err := store.GetPaymentChannel(r.Context(), d.DB, id); errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, preparedPaymentChannelResponse{
				ID:         id,
				WebhookURL: paymentWebhookURL(r, id),
			})
			return
		} else if err != nil {
			writePaymentError(w, err)
			return
		}
	}
	writeError(w, http.StatusInternalServerError, errors.New("could not allocate payment channel id"))
}

func createPaymentChannelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body paymentChannelPayload
	if err := decodeJSON(r, &body); err != nil || body.Name == nil || body.Provider == nil || body.Config == nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	name := strings.TrimSpace(*body.Name)
	if name == "" || len(name) > 120 {
		writeError(w, http.StatusBadRequest, errors.New("payment channel name is required"))
		return
	}
	id := ""
	if body.ID != nil {
		id = strings.TrimSpace(*body.ID)
		if !validPreparedRecordID(id, "paych") {
			writeError(w, http.StatusBadRequest, errors.New("invalid_payment_channel_id"))
			return
		}
	}
	provider, err := normalizePaymentProvider(*body.Provider)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	merged, err := mergePaymentChannelConfig(provider, nil, *body.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	config, err := normalizePaymentChannelConfigForState(provider, merged, enabled)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := validatePaymentChannelSettlementConfig(provider, config, globalSettlementCurrency(d)); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	environmentInput := ""
	if body.Environment != nil {
		environmentInput = *body.Environment
	}
	environment, err := normalizePaymentEnvironment(provider, config, environmentInput)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sortOrder := 0
	if body.SortOrder != nil {
		sortOrder = *body.SortOrder
	}
	created, err := store.CreatePaymentChannel(r.Context(), d.DB, store.PaymentChannel{
		ID: id, Name: name, Provider: provider, Environment: environment, Config: config, Enabled: enabled, SortOrder: sortOrder,
	})
	if err != nil {
		writePaymentError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, adminPaymentChannelJSON(*created, r))
}

func updatePaymentChannelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(pathParam(r, "id"))
	existing, err := store.GetPaymentChannel(r.Context(), d.DB, id)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	var body paymentChannelPayload
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if paymentChannelDisableOnly(body) {
		updated, updateErr := store.UpdatePaymentChannel(r.Context(), d.DB, id, store.PaymentChannelPatch{Enabled: body.Enabled})
		if updateErr != nil {
			writePaymentError(w, updateErr)
			return
		}
		writeJSON(w, http.StatusOK, adminPaymentChannelJSON(*updated, r))
		return
	}
	provider := existing.Provider
	if body.Provider != nil {
		provider, err = normalizePaymentProvider(*body.Provider)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	providerChanged := provider != existing.Provider
	if providerChanged {
		hasMethods, checkErr := store.HasPaymentMethodsByChannel(r.Context(), d.DB, id)
		if checkErr != nil {
			writePaymentError(w, checkErr)
			return
		}
		if hasMethods {
			writeError(w, http.StatusConflict, errors.New("remove bound payment methods before changing the provider"))
			return
		}
	}

	enabled := existing.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	config := existing.Config
	configChanged := false
	if body.Config != nil {
		base := existing.Config
		if providerChanged {
			base = nil
		}
		merged, mergeErr := mergePaymentChannelConfig(provider, base, *body.Config)
		if mergeErr != nil {
			writeError(w, http.StatusBadRequest, mergeErr)
			return
		}
		config, err = normalizePaymentChannelConfigForState(provider, merged, enabled)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		current, currentErr := normalizePaymentChannelConfigForState(existing.Provider, existing.Config, existing.Enabled)
		configChanged = currentErr != nil || providerChanged || !bytes.Equal(config, current)
	} else if providerChanged {
		writeError(w, http.StatusBadRequest, errors.New("configuration is required when changing provider"))
		return
	} else {
		config, err = normalizePaymentChannelConfigForState(provider, config, enabled)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	environmentInput := existing.Environment
	if body.Environment != nil {
		environmentInput = *body.Environment
	}
	environment, err := normalizePaymentEnvironment(provider, config, environmentInput)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	environmentChanged := environment != existing.Environment
	if err := validatePaymentChannelSettlementConfig(provider, config, globalSettlementCurrency(d)); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if providerChanged || configChanged || environmentChanged {
		hasPending, checkErr := store.HasPendingPaymentOrdersByChannel(r.Context(), d.DB, id)
		if checkErr != nil {
			writePaymentError(w, checkErr)
			return
		}
		if hasPending {
			writeError(w, http.StatusConflict, store.ErrPaymentChannelHasPending)
			return
		}
	}

	patch := store.PaymentChannelPatch{Enabled: body.Enabled, SortOrder: body.SortOrder}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" || len(name) > 120 {
			writeError(w, http.StatusBadRequest, errors.New("payment channel name is required"))
			return
		}
		patch.Name = &name
	}
	if body.Provider != nil {
		patch.Provider = &provider
	}
	if body.Environment != nil || environmentChanged {
		patch.Environment = &environment
	}
	if body.Config != nil {
		patch.Config = &config
	}
	updated, err := store.UpdatePaymentChannel(r.Context(), d.DB, id, patch)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, adminPaymentChannelJSON(*updated, r))
}

func paymentChannelDisableOnly(body paymentChannelPayload) bool {
	return body.Enabled != nil && !*body.Enabled && body.ID == nil && body.Name == nil &&
		body.Provider == nil && body.Environment == nil && body.Config == nil && body.SortOrder == nil
}

func deletePaymentChannelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	if err := store.DeletePaymentChannel(r.Context(), d.DB, strings.TrimSpace(pathParam(r, "id"))); err != nil {
		writePaymentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type paymentMethodPayload struct {
	Name                 *string          `json:"name"`
	Icon                 *string          `json:"icon"`
	ChannelID            *string          `json:"channel_id"`
	ProviderMethodConfig *json.RawMessage `json:"provider_method_config"`
	Enabled              *bool            `json:"enabled"`
	SortOrder            *int             `json:"sort_order"`
}

type adminPaymentMethodResponse struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	Icon                 string          `json:"icon"`
	ChannelID            string          `json:"channel_id"`
	Provider             string          `json:"provider"`
	ProviderMethodConfig json.RawMessage `json:"provider_method_config"`
	Enabled              bool            `json:"enabled"`
	SortOrder            int             `json:"sort_order"`
	CreatedAt            int64           `json:"created_at"`
	UpdatedAt            int64           `json:"updated_at"`
}

func adminPaymentMethodJSON(method store.PaymentMethod, provider string) adminPaymentMethodResponse {
	return adminPaymentMethodResponse{
		ID: method.ID, Name: method.Name, Icon: method.Icon, ChannelID: method.ChannelID,
		Provider: provider, ProviderMethodConfig: method.ProviderMethodConfig,
		Enabled: method.Enabled, SortOrder: method.SortOrder,
		CreatedAt: method.CreatedAt, UpdatedAt: method.UpdatedAt,
	}
}

func paymentMethodProviderMap(d Deps, r *http.Request) (map[string]string, error) {
	channels, err := store.ListPaymentChannels(r.Context(), d.DB)
	if err != nil {
		return nil, err
	}
	providers := make(map[string]string, len(channels))
	for _, channel := range channels {
		providers[channel.ID] = channel.Provider
	}
	return providers, nil
}

func listPaymentMethodsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	methods, err := store.ListPaymentMethods(r.Context(), d.DB, strings.TrimSpace(r.URL.Query().Get("channel_id")))
	if err != nil {
		writePaymentError(w, err)
		return
	}
	providers, err := paymentMethodProviderMap(d, r)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	response := make([]adminPaymentMethodResponse, 0, len(methods))
	for _, method := range methods {
		response = append(response, adminPaymentMethodJSON(method, providers[method.ChannelID]))
	}
	writeJSON(w, http.StatusOK, response)
}

func validatePaymentMethodText(name, icon string) error {
	if name == "" || len(name) > 120 {
		return errors.New("payment method name is required")
	}
	if len(icon) > 128 {
		return errors.New("payment method icon is too long")
	}
	return nil
}

func createPaymentMethodAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body paymentMethodPayload
	if err := decodeJSON(r, &body); err != nil || body.Name == nil || body.ChannelID == nil || body.ProviderMethodConfig == nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	name := strings.TrimSpace(*body.Name)
	icon := ""
	if body.Icon != nil {
		icon = strings.TrimSpace(*body.Icon)
	}
	if err := validatePaymentMethodText(name, icon); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	channel, err := store.GetPaymentChannel(r.Context(), d.DB, strings.TrimSpace(*body.ChannelID))
	if err != nil {
		writePaymentError(w, err)
		return
	}
	config, err := normalizePaymentMethodConfig(channel.Provider, *body.ProviderMethodConfig)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if err := validateEnabledPaymentMethodChannel(*channel, config, enabled); err != nil {
		writePaymentError(w, err)
		return
	}
	sortOrder := 0
	if body.SortOrder != nil {
		sortOrder = *body.SortOrder
	}
	created, err := store.CreatePaymentMethod(r.Context(), d.DB, store.PaymentMethod{
		ChannelID: channel.ID, Name: name, Type: channel.Provider, Icon: icon,
		ProviderMethodConfig: config, Enabled: enabled, SortOrder: sortOrder,
	})
	if err != nil {
		writePaymentError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, adminPaymentMethodJSON(*created, channel.Provider))
}

func updatePaymentMethodAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(pathParam(r, "id"))
	existing, err := store.GetPaymentMethod(r.Context(), d.DB, id)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	var body paymentMethodPayload
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	channelID := existing.ChannelID
	if body.ChannelID != nil {
		channelID = strings.TrimSpace(*body.ChannelID)
	}
	channel, err := store.GetPaymentChannel(r.Context(), d.DB, channelID)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	name := existing.Name
	if body.Name != nil {
		name = strings.TrimSpace(*body.Name)
	}
	icon := existing.Icon
	if body.Icon != nil {
		icon = strings.TrimSpace(*body.Icon)
	}
	if err := validatePaymentMethodText(name, icon); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	config := existing.ProviderMethodConfig
	if body.ProviderMethodConfig != nil {
		config = *body.ProviderMethodConfig
	}
	config, err = normalizePaymentMethodConfig(channel.Provider, config)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	enabled := existing.Enabled
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if err := validateEnabledPaymentMethodChannel(*channel, config, enabled); err != nil {
		writePaymentError(w, err)
		return
	}
	methodType := channel.Provider
	patch := store.PaymentMethodPatch{
		Name: &name, Icon: &icon, ChannelID: &channelID, Type: &methodType,
		ProviderMethodConfig: &config, Enabled: body.Enabled, SortOrder: body.SortOrder,
	}
	updated, err := store.UpdatePaymentMethod(r.Context(), d.DB, id, patch)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, adminPaymentMethodJSON(*updated, channel.Provider))
}

func validateEnabledPaymentMethodChannel(channel store.PaymentChannel, methodConfig json.RawMessage, enabled bool) error {
	if !enabled {
		return nil
	}
	if !channel.Enabled {
		return store.ErrPaymentMethodUnavailable
	}
	if _, err := paymentGateway(channel.Provider, channel.Config, methodConfig); err != nil {
		return store.ErrPaymentMethodUnavailable
	}
	return nil
}

func reorderPaymentMethodsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	methods, err := store.ListPaymentMethods(r.Context(), d.DB, "")
	if err != nil {
		writePaymentError(w, err)
		return
	}
	if len(body.IDs) != len(methods) {
		writeError(w, http.StatusBadRequest, errors.New("payment method reorder list must include every method"))
		return
	}
	valid := make(map[string]bool, len(methods))
	for _, method := range methods {
		valid[method.ID] = true
	}
	seen := map[string]bool{}
	for _, id := range body.IDs {
		if !valid[id] || seen[id] {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
		seen[id] = true
	}
	if err := store.ReorderPaymentMethods(r.Context(), d.DB, body.IDs); err != nil {
		writePaymentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func deletePaymentMethodAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	if err := store.DeletePaymentMethod(r.Context(), d.DB, strings.TrimSpace(pathParam(r, "id"))); err != nil {
		writePaymentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type adminPaymentOrderResponse struct {
	ID                             string  `json:"id"`
	UserEmail                      string  `json:"user_email"`
	TargetType                     string  `json:"target_type"`
	TargetName                     string  `json:"target_name"`
	BillingCycle                   string  `json:"billing_cycle"`
	AmountMinor                    int64   `json:"amount_minor"`
	TaxAmountMinor                 int64   `json:"tax_amount_minor,omitempty"`
	Currency                       string  `json:"currency"`
	ProviderAmountMinor            int64   `json:"provider_amount_minor"`
	ProviderCurrency               string  `json:"provider_currency"`
	ConversionRate                 string  `json:"conversion_rate,omitempty"`
	ChannelName                    string  `json:"channel_name"`
	MethodName                     string  `json:"method_name"`
	Provider                       string  `json:"provider"`
	Environment                    string  `json:"environment"`
	ProviderOrderID                string  `json:"provider_order_id,omitempty"`
	ProviderPaymentID              string  `json:"provider_payment_id,omitempty"`
	CheckoutSessionID              string  `json:"checkout_session_id,omitempty"`
	CheckoutExpiresAt              *int64  `json:"checkout_expires_at"`
	LastReconciledAt               *int64  `json:"last_reconciled_at"`
	ReconcileError                 *string `json:"reconcile_error,omitempty"`
	Status                         string  `json:"status"`
	CanDelete                      bool    `json:"can_delete"`
	DeleteNeedsGatewayConfirmation bool    `json:"delete_requires_gateway_confirmation,omitempty"`
	CreatedAt                      int64   `json:"created_at"`
	PaidAt                         *int64  `json:"paid_at"`
	FulfilledAt                    *int64  `json:"fulfilled_at"`
	FailureReason                  *string `json:"failure_reason,omitempty"`
}

func nonzeroPaymentTime(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

func adminPaymentOrderJSON(order store.PaymentOrder) adminPaymentOrderResponse {
	var failure *string
	reason := strings.TrimSpace(order.FailureMessage)
	if reason == "" && order.FailureCode != "admin_manual_close" && order.FailureCode != "admin_reconciled_close" {
		reason = strings.TrimSpace(order.FailureCode)
	}
	if reason != "" {
		failure = &reason
	}
	var reconcileError *string
	if value := strings.TrimSpace(order.ReconcileError); value != "" {
		reconcileError = &value
	}
	displayAmount, displayCurrency := paymentOrderDisplayAmountCurrency(order)
	canDelete, deleteNeedsGatewayConfirmation := store.PaymentOrderDeletePolicy(order)
	return adminPaymentOrderResponse{
		ID: order.ID, UserEmail: order.UserEmail, TargetType: order.ProductType,
		TargetName: order.ProductName, BillingCycle: order.BillingCycle,
		AmountMinor: displayAmount, TaxAmountMinor: order.TaxAmountMinor,
		Currency: displayCurrency, ProviderAmountMinor: order.ProviderAmountMinor,
		ProviderCurrency: order.ProviderCurrency, ConversionRate: order.ConversionRate,
		ChannelName: order.ChannelName, MethodName: order.MethodName,
		Provider: order.Provider, Environment: order.Environment,
		ProviderOrderID: order.ProviderOrderID, ProviderPaymentID: order.ProviderPaymentID,
		CheckoutSessionID: order.CheckoutSessionID,
		CheckoutExpiresAt: nonzeroPaymentTime(order.CheckoutExpiresAt),
		LastReconciledAt:  nonzeroPaymentTime(order.LastReconciledAt), ReconcileError: reconcileError,
		Status: order.Status, CanDelete: canDelete, DeleteNeedsGatewayConfirmation: deleteNeedsGatewayConfirmation,
		CreatedAt: order.CreatedAt,
		PaidAt:    nonzeroPaymentTime(order.PaidAt), FulfilledAt: nonzeroPaymentTime(order.FulfilledAt),
		FailureReason: failure,
	}
}

func reconcilePaymentOrderAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	orderID := strings.TrimSpace(pathParam(r, "id"))
	order, err := store.GetPaymentOrder(r.Context(), d.DB, orderID)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	if order.Status != store.PaymentOrderPending && order.Status != store.PaymentOrderProcessing {
		writePaymentError(w, store.ErrPaymentOrderNotMutable)
		return
	}
	var body struct {
		Action  string `json:"action"`
		Reason  string `json:"reason"`
		Confirm bool   `json:"confirm"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	body.Action = strings.ToLower(strings.TrimSpace(body.Action))
	if body.Action == "" {
		body.Action = "reconcile"
	}
	if body.Action != "reconcile" && body.Action != "close" {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	closeOrder := body.Action == "close"
	body.Reason = strings.TrimSpace(body.Reason)
	if closeOrder && !body.Confirm {
		writeError(w, http.StatusBadRequest, errors.New("closing a payment order requires confirmation"))
		return
	}
	if closeOrder && utf8.RuneCountInString(body.Reason) > 500 {
		writeError(w, http.StatusBadRequest, errors.New("payment order closure reason must not exceed 500 characters"))
		return
	}
	channel, err := store.GetPaymentChannel(r.Context(), d.DB, order.ChannelID)
	if err != nil || channel.Provider != order.Provider || channel.Environment != order.Environment {
		writeError(w, http.StatusConflict, errors.New("payment channel no longer matches the order snapshot"))
		return
	}

	if order.Provider == paymentcore.ProviderEPay {
		if !closeOrder {
			writeError(w, http.StatusConflict, paymentcore.ErrReconciliationUnsupported)
			return
		}
		closed, err := store.CancelPaymentOrderByAdmin(r.Context(), d.DB, order.ID, body.Reason)
		if err != nil {
			writePaymentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, adminPaymentOrderJSON(*closed))
		return
	}

	reconcileRequest := paymentcore.ReconcileRequest{
		OrderID: order.ID, UserID: order.UserID, AmountMinor: order.ProviderAmountMinor, Currency: order.ProviderCurrency,
		SessionID: order.CheckoutSessionID, SessionExpiresAt: order.CheckoutExpiresAt, Close: closeOrder,
	}
	var event paymentcore.ProviderEvent
	switch order.Provider {
	case paymentcore.ProviderStripe:
		var config paymentcore.StripeConfig
		if json.Unmarshal(channel.Config, &config) != nil || paymentcore.ValidateStripeConfig(config) != nil {
			writeError(w, http.StatusConflict, errors.New("invalid Stripe channel configuration"))
			return
		}
		event, err = (paymentcore.StripeReconciler{Config: config}).Reconcile(r.Context(), reconcileRequest)
	case paymentcore.ProviderWaffo:
		var config paymentcore.WaffoConfig
		if json.Unmarshal(channel.Config, &config) != nil || paymentcore.ValidateWaffoConfig(config) != nil {
			writeError(w, http.StatusConflict, errors.New("invalid Waffo channel configuration"))
			return
		}
		event, err = (paymentcore.WaffoReconciler{Config: config}).Reconcile(r.Context(), reconcileRequest)
	default:
		err = paymentcore.ErrReconciliationUnsupported
	}
	if err != nil {
		_, _ = store.MarkPaymentOrderReconciled(r.Context(), d.DB, order.ID, err.Error())
		status := http.StatusBadGateway
		if errors.Is(err, paymentcore.ErrCheckoutNotClosable) || errors.Is(err, paymentcore.ErrReconciliationUnsupported) ||
			errors.Is(err, paymentcore.ErrCheckoutStateUnknown) {
			status = http.StatusConflict
		}
		writeError(w, status, err)
		return
	}
	appliedOrder, err := applyProviderEvent(r, d, order.Provider, order.ChannelID, event)
	if err != nil {
		_, _ = store.MarkPaymentOrderReconciled(r.Context(), d.DB, order.ID, err.Error())
		writePaymentError(w, err)
		return
	}
	if appliedOrder != nil && appliedOrder.UserID != "" {
		invalidateAuthUser(d, appliedOrder.UserID)
	}
	updated, err := store.MarkPaymentOrderReconciled(r.Context(), d.DB, order.ID, "")
	if err != nil {
		writePaymentError(w, err)
		return
	}
	if closeOrder && (updated.Status == store.PaymentOrderExpired || updated.Status == store.PaymentOrderCancelled) {
		updated, err = store.AnnotateClosedPaymentOrder(r.Context(), d.DB, order.ID, "admin_reconciled_close", body.Reason)
		if err != nil {
			writePaymentError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, adminPaymentOrderJSON(*updated))
}

func validPaymentOrderStatus(status string) bool {
	switch status {
	case "", store.PaymentOrderPending, store.PaymentOrderProcessing, store.PaymentOrderFulfilled,
		store.PaymentOrderFailed, store.PaymentOrderExpired, store.PaymentOrderCancelled:
		return true
	default:
		return false
	}
}

func listPaymentOrdersAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	status := strings.ToLower(strings.TrimSpace(query.Get("status")))
	provider := strings.ToLower(strings.TrimSpace(query.Get("provider")))
	if !validPaymentOrderStatus(status) {
		writeError(w, http.StatusBadRequest, errors.New("invalid payment order status"))
		return
	}
	if provider != "" {
		var err error
		provider, err = normalizePaymentProvider(provider)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	limit := 100
	if raw := query.Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		} else {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
	}
	if limit < 1 || limit > 500 {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	offset := 0
	if raw := query.Get("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			offset = parsed
		} else {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
	}
	if offset < 0 {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	filter := store.PaymentOrderFilter{
		Status: status, Provider: provider, Search: query.Get("search"), Limit: limit, Offset: offset,
	}
	orders, err := store.ListPaymentOrders(r.Context(), d.DB, filter)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	total, err := store.CountPaymentOrders(r.Context(), d.DB, filter)
	if err != nil {
		writePaymentError(w, err)
		return
	}
	response := make([]adminPaymentOrderResponse, 0, len(orders))
	for _, order := range orders {
		response = append(response, adminPaymentOrderJSON(order))
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": response, "total": total})
}

func deletePaymentOrderAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	orderID := strings.TrimSpace(pathParam(r, "id"))
	gatewayFinalAcknowledged := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("gateway_final_acknowledged")), "true")
	adminID := ""
	if admin := authUser(r); admin != nil {
		adminID = admin.ID
	}
	if err := store.DeletePaymentOrder(r.Context(), d.DB, orderID, gatewayFinalAcknowledged); err != nil {
		slog.Warn("admin payment order permanent deletion rejected", "admin_id", adminID, "order_id", orderID, "gateway_final_acknowledged", gatewayFinalAcknowledged, "result", "rejected", "err", err)
		writePaymentError(w, err)
		return
	}
	slog.Info("admin permanently deleted payment order", "admin_id", adminID, "order_id", orderID, "gateway_final_acknowledged", gatewayFinalAcknowledged, "result", "deleted")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
