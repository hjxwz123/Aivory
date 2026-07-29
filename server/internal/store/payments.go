package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	paymentcore "aivory/server/internal/payment"
)

const (
	PaymentProductCreditPackage = "credit_package"
	PaymentProductUserGroup     = "user_group"

	PaymentBillingMonthly = "monthly"
	PaymentBillingYearly  = "yearly"

	PaymentEnvironmentLive = "live"
	PaymentEnvironmentTest = "test"

	PaymentOrderPending    = "pending"
	PaymentOrderProcessing = "processing"
	PaymentOrderFulfilled  = "fulfilled"
	PaymentOrderFailed     = "failed"
	PaymentOrderCancelled  = "cancelled"
	PaymentOrderExpired    = "expired"

	PaymentOrderAttemptIssued = "issued"
	PaymentOrderAttemptPaid   = "paid"
)

var (
	ErrInvalidPaymentChannel          = errors.New("invalid_payment_channel")
	ErrPaymentChannelNameExists       = errors.New("payment_channel_name_exists")
	ErrPaymentChannelIDExists         = errors.New("payment_channel_id_exists")
	ErrPaymentChannelHasMethods       = errors.New("payment_channel_has_methods")
	ErrPaymentChannelHasPending       = errors.New("payment_channel_has_pending_orders")
	ErrInvalidPaymentMethod           = errors.New("invalid_payment_method")
	ErrPaymentMethodNameExists        = errors.New("payment_method_name_exists")
	ErrPaymentMethodUnavailable       = errors.New("payment_method_unavailable")
	ErrInvalidPaymentProduct          = errors.New("invalid_payment_product")
	ErrPaymentProductUnavailable      = errors.New("payment_product_unavailable")
	ErrPaymentUserGroupNotPurchasable = errors.New("payment_user_group_not_purchasable")
	ErrPaymentUserUnavailable         = errors.New("payment_user_unavailable")
	ErrPaymentUserGroupPermanent      = errors.New("payment_user_group_already_permanent")
	ErrPaymentOrderNotMutable         = errors.New("payment_order_not_mutable")
	ErrPaymentOrderNotDeletable       = errors.New("payment_order_not_deletable")
	ErrPaymentOrderDeleteNeedsAck     = errors.New("payment_order_delete_requires_gateway_confirmation")
	ErrPaymentOrderNotFulfillable     = errors.New("payment_order_not_fulfillable")
	ErrPaymentProviderOrderConflict   = errors.New("payment_provider_order_conflict")
	ErrPaymentProviderOrderMismatch   = errors.New("payment_provider_order_mismatch")
	ErrPaymentAmountMismatch          = errors.New("payment_amount_mismatch")
	ErrPaymentCurrencyMismatch        = errors.New("payment_currency_mismatch")
	ErrPaymentEventConflict           = errors.New("payment_event_conflict")
	ErrInvalidPaymentEvent            = errors.New("invalid_payment_event")
	ErrPaymentOrdersPendingForGroup   = errors.New("payment_orders_pending_for_group")
	ErrPaymentOrdersPendingForUser    = errors.New("payment_orders_pending_for_user")
)

// PaymentChannel is one configured provider account. Config is intentionally
// returned verbatim to the admin API; redaction belongs at that boundary.
type PaymentChannel struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Provider    string          `json:"provider"`
	Environment string          `json:"environment"`
	Config      json.RawMessage `json:"config"`
	Enabled     bool            `json:"enabled"`
	SortOrder   int             `json:"sort_order"`
	CreatedAt   int64           `json:"created_at"`
	UpdatedAt   int64           `json:"updated_at"`
}

type PaymentChannelPatch struct {
	Name        *string          `json:"name"`
	Provider    *string          `json:"provider"`
	Environment *string          `json:"environment"`
	Config      *json.RawMessage `json:"config"`
	Enabled     *bool            `json:"enabled"`
	SortOrder   *int             `json:"sort_order"`
}

const paymentChannelCols = `id, name, provider, environment, config, enabled, sort_order, created_at, updated_at`

func scanPaymentChannel(s scanner) (PaymentChannel, error) {
	var channel PaymentChannel
	var config string
	var enabled int
	if err := s.Scan(
		&channel.ID,
		&channel.Name,
		&channel.Provider,
		&channel.Environment,
		&config,
		&enabled,
		&channel.SortOrder,
		&channel.CreatedAt,
		&channel.UpdatedAt,
	); err != nil {
		return channel, err
	}
	channel.Config = json.RawMessage(config)
	channel.Enabled = enabled != 0
	return channel, nil
}

func ListPaymentChannels(ctx context.Context, db *sql.DB) ([]PaymentChannel, error) {
	rows, err := db.QueryContext(ctx, `SELECT `+paymentChannelCols+` FROM payment_channels ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	channels := []PaymentChannel{}
	for rows.Next() {
		channel, err := scanPaymentChannel(rows)
		if err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	return channels, rows.Err()
}

func GetPaymentChannel(ctx context.Context, db *sql.DB, id string) (*PaymentChannel, error) {
	channel, err := scanPaymentChannel(db.QueryRowContext(ctx,
		`SELECT `+paymentChannelCols+` FROM payment_channels WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &channel, nil
}

func CreatePaymentChannel(ctx context.Context, db *sql.DB, channel PaymentChannel) (*PaymentChannel, error) {
	channel.Name = strings.TrimSpace(channel.Name)
	channel.Provider = normalizePaymentIdentifier(channel.Provider)
	channel.Environment = normalizePaymentIdentifier(channel.Environment)
	if channel.Environment == "" {
		channel.Environment = PaymentEnvironmentLive
	}
	if channel.Name == "" || channel.Provider == "" || !validPaymentEnvironment(channel.Environment) {
		return nil, ErrInvalidPaymentChannel
	}
	if channel.ID == "" {
		channel.ID = genID("paych")
	}
	now := time.Now().Unix()
	_, err := db.ExecContext(ctx,
		`INSERT INTO payment_channels(id, name, provider, environment, config, enabled, sort_order, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		channel.ID, channel.Name, channel.Provider, channel.Environment, paymentJSONText(channel.Config), boolInt(channel.Enabled),
		channel.SortOrder, now, now)
	if err != nil {
		if isUniqueIndexErr(err, "idx_payment_channels_name_unique", "payment_channels.name") {
			return nil, ErrPaymentChannelNameExists
		}
		if isUniqueIndexErr(err, "payment_channels.id", "payment_channels_pkey") {
			return nil, ErrPaymentChannelIDExists
		}
		return nil, err
	}
	return GetPaymentChannel(ctx, db, channel.ID)
}

func ReorderPaymentChannels(ctx context.Context, db *sql.DB, ids []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for index, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE payment_channels SET sort_order=?, updated_at=? WHERE id=?`, index, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func UpdatePaymentChannel(ctx context.Context, db *sql.DB, id string, patch PaymentChannelPatch) (*PaymentChannel, error) {
	// Channel mutations must share the same lock as checkout creation. The API
	// performs friendly validation before calling this function, but the store
	// is the authority for the pending-order guard: a checkout can otherwise be
	// inserted between an API-side preflight and the UPDATE.
	if patch.Name == nil && patch.Provider == nil && patch.Environment == nil && patch.Config == nil &&
		patch.Enabled == nil && patch.SortOrder == nil {
		return GetPaymentChannel(ctx, db, id)
	}
	tx, err := db.BeginTx(ctx, paymentAdminWriteTxOptions())
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	channelQuery := `SELECT ` + paymentChannelCols + ` FROM payment_channels WHERE id=?`
	if usePostgres {
		channelQuery += ` FOR UPDATE`
	}
	current, err := scanPaymentChannel(tx.QueryRowContext(ctx, channelQuery, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	next := current
	parts := []string{}
	args := []any{}
	set := func(column string, value any) {
		parts = append(parts, column+"=?")
		args = append(args, value)
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return nil, ErrInvalidPaymentChannel
		}
		next.Name = name
		set("name", name)
	}
	if patch.Provider != nil {
		provider := normalizePaymentIdentifier(*patch.Provider)
		if provider == "" {
			return nil, ErrInvalidPaymentChannel
		}
		next.Provider = provider
		set("provider", provider)
	}
	if patch.Environment != nil {
		environment := normalizePaymentIdentifier(*patch.Environment)
		if !validPaymentEnvironment(environment) {
			return nil, ErrInvalidPaymentChannel
		}
		next.Environment = environment
		set("environment", environment)
	}
	if patch.Config != nil {
		next.Config = json.RawMessage(paymentJSONText(*patch.Config))
		set("config", string(next.Config))
	}
	if patch.Enabled != nil {
		next.Enabled = *patch.Enabled
		set("enabled", boolInt(*patch.Enabled))
	}
	if patch.SortOrder != nil {
		next.SortOrder = *patch.SortOrder
		set("sort_order", *patch.SortOrder)
	}
	providerChanged := next.Provider != current.Provider
	configChanged := string(next.Config) != string(current.Config)
	environmentChanged := next.Environment != current.Environment
	if providerChanged {
		var methods int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_methods WHERE channel_id=?`, id).Scan(&methods); err != nil {
			return nil, err
		}
		if methods > 0 {
			return nil, ErrPaymentChannelHasMethods
		}
	}
	if providerChanged || configChanged || environmentChanged {
		var pending int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_orders WHERE channel_id=? AND status IN (?, ?)`,
			id, PaymentOrderPending, PaymentOrderProcessing).Scan(&pending); err != nil {
			return nil, err
		}
		if pending > 0 {
			return nil, ErrPaymentChannelHasPending
		}
	}
	if len(parts) == 0 {
		return &current, nil
	}
	set("updated_at", time.Now().Unix())
	args = append(args, id)
	result, err := tx.ExecContext(ctx,
		fmt.Sprintf(`UPDATE payment_channels SET %s WHERE id=?`, strings.Join(parts, ", ")), args...)
	if err != nil {
		if isUniqueIndexErr(err, "idx_payment_channels_name_unique", "payment_channels.name") {
			return nil, ErrPaymentChannelNameExists
		}
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return GetPaymentChannel(ctx, db, id)
}

func CountPaymentMethodsByChannel(ctx context.Context, db *sql.DB, channelID string) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_methods WHERE channel_id=?`, channelID).Scan(&count)
	return count, err
}

func HasPaymentMethodsByChannel(ctx context.Context, db *sql.DB, channelID string) (bool, error) {
	count, err := CountPaymentMethodsByChannel(ctx, db, channelID)
	return count > 0, err
}

func DeletePaymentChannel(ctx context.Context, db *sql.DB, id string) error {
	// Lock the channel before checking orders/methods. CreatePaymentOrder takes
	// a share lock on this row, so the guard observes either the checkout before
	// this deletion or the deletion before a later checkout, never a stale gap.
	tx, err := db.BeginTx(ctx, paymentAdminWriteTxOptions())
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	channelQuery := `SELECT id FROM payment_channels WHERE id=?`
	if usePostgres {
		channelQuery += ` FOR UPDATE`
	}
	var channelID string
	if err := tx.QueryRowContext(ctx, channelQuery, id).Scan(&channelID); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	var pending int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_orders WHERE channel_id=? AND status IN (?, ?)`,
		id, PaymentOrderPending, PaymentOrderProcessing).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return ErrPaymentChannelHasPending
	}
	var methods int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_methods WHERE channel_id=?`, id).Scan(&methods); err != nil {
		return err
	}
	if methods > 0 {
		return ErrPaymentChannelHasMethods
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM payment_channels WHERE id=?`, id)
	if err != nil {
		if isForeignKeyConstraintErr(err) {
			return ErrPaymentChannelHasMethods
		}
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// PaymentMethod is an admin-managed payment choice attached to a channel.
// Config remains private; ListEnabledPaymentMethods returns a safe projection.
type PaymentMethod struct {
	ID                   string          `json:"id"`
	ChannelID            string          `json:"channel_id"`
	Name                 string          `json:"name"`
	Type                 string          `json:"type"`
	Icon                 string          `json:"icon"`
	ProviderMethodConfig json.RawMessage `json:"provider_method_config"`
	Enabled              bool            `json:"enabled"`
	SortOrder            int             `json:"sort_order"`
	CreatedAt            int64           `json:"created_at"`
	UpdatedAt            int64           `json:"updated_at"`
}

type PaymentMethodPatch struct {
	ChannelID            *string          `json:"channel_id"`
	Name                 *string          `json:"name"`
	Type                 *string          `json:"type"`
	Icon                 *string          `json:"icon"`
	ProviderMethodConfig *json.RawMessage `json:"provider_method_config"`
	Enabled              *bool            `json:"enabled"`
	SortOrder            *int             `json:"sort_order"`
}

type EnabledPaymentMethod struct {
	ID          string `json:"id"`
	ChannelID   string `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	Provider    string `json:"provider"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Icon        string `json:"icon"`
	SortOrder   int    `json:"sort_order"`
}

const paymentMethodCols = `id, channel_id, name, type, icon, config, enabled, sort_order, created_at, updated_at`

func scanPaymentMethod(s scanner) (PaymentMethod, error) {
	var method PaymentMethod
	var config string
	var enabled int
	if err := s.Scan(
		&method.ID,
		&method.ChannelID,
		&method.Name,
		&method.Type,
		&method.Icon,
		&config,
		&enabled,
		&method.SortOrder,
		&method.CreatedAt,
		&method.UpdatedAt,
	); err != nil {
		return method, err
	}
	method.ProviderMethodConfig = json.RawMessage(config)
	method.Enabled = enabled != 0
	return method, nil
}

func ListPaymentMethods(ctx context.Context, db *sql.DB, channelID string) ([]PaymentMethod, error) {
	query := `SELECT ` + paymentMethodCols + ` FROM payment_methods`
	args := []any{}
	if channelID != "" {
		query += ` WHERE channel_id=?`
		args = append(args, channelID)
	}
	query += ` ORDER BY sort_order, name`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	methods := []PaymentMethod{}
	for rows.Next() {
		method, err := scanPaymentMethod(rows)
		if err != nil {
			return nil, err
		}
		methods = append(methods, method)
	}
	return methods, rows.Err()
}

func ListEnabledPaymentMethods(ctx context.Context, db *sql.DB) ([]EnabledPaymentMethod, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT pm.id, pm.channel_id, pc.name, pc.provider, pm.name, pm.type, pm.icon, pm.sort_order
		   FROM payment_methods pm
		   JOIN payment_channels pc ON pc.id=pm.channel_id
		  WHERE pm.enabled=1 AND pc.enabled=1
		  ORDER BY pm.sort_order, pc.sort_order, pm.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	methods := []EnabledPaymentMethod{}
	for rows.Next() {
		var method EnabledPaymentMethod
		if err := rows.Scan(
			&method.ID,
			&method.ChannelID,
			&method.ChannelName,
			&method.Provider,
			&method.Name,
			&method.Type,
			&method.Icon,
			&method.SortOrder,
		); err != nil {
			return nil, err
		}
		methods = append(methods, method)
	}
	return methods, rows.Err()
}

func GetPaymentMethod(ctx context.Context, db *sql.DB, id string) (*PaymentMethod, error) {
	method, err := scanPaymentMethod(db.QueryRowContext(ctx,
		`SELECT `+paymentMethodCols+` FROM payment_methods WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &method, nil
}

func CreatePaymentMethod(ctx context.Context, db *sql.DB, method PaymentMethod) (*PaymentMethod, error) {
	method.ChannelID = strings.TrimSpace(method.ChannelID)
	method.Name = strings.TrimSpace(method.Name)
	method.Type = normalizePaymentIdentifier(method.Type)
	method.Icon = strings.TrimSpace(method.Icon)
	if method.ChannelID == "" || method.Name == "" || method.Type == "" {
		return nil, ErrInvalidPaymentMethod
	}
	if _, err := GetPaymentChannel(ctx, db, method.ChannelID); err != nil {
		return nil, err
	}
	if method.ID == "" {
		method.ID = genID("paym")
	}
	now := time.Now().Unix()
	_, err := db.ExecContext(ctx,
		`INSERT INTO payment_methods(id, channel_id, name, type, icon, config, enabled, sort_order, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		method.ID, method.ChannelID, method.Name, method.Type, method.Icon, paymentJSONText(method.ProviderMethodConfig), boolInt(method.Enabled),
		method.SortOrder, now, now)
	if err != nil {
		if isUniqueIndexErr(err, "idx_payment_methods_channel_name_unique", "payment_methods.channel_id") {
			return nil, ErrPaymentMethodNameExists
		}
		return nil, err
	}
	return GetPaymentMethod(ctx, db, method.ID)
}

func UpdatePaymentMethod(ctx context.Context, db *sql.DB, id string, patch PaymentMethodPatch) (*PaymentMethod, error) {
	parts := []string{}
	args := []any{}
	set := func(column string, value any) {
		parts = append(parts, column+"=?")
		args = append(args, value)
	}
	if patch.ChannelID != nil {
		channelID := strings.TrimSpace(*patch.ChannelID)
		if channelID == "" {
			return nil, ErrInvalidPaymentMethod
		}
		if _, err := GetPaymentChannel(ctx, db, channelID); err != nil {
			return nil, err
		}
		set("channel_id", channelID)
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return nil, ErrInvalidPaymentMethod
		}
		set("name", name)
	}
	if patch.Type != nil {
		methodType := normalizePaymentIdentifier(*patch.Type)
		if methodType == "" {
			return nil, ErrInvalidPaymentMethod
		}
		set("type", methodType)
	}
	if patch.Icon != nil {
		set("icon", strings.TrimSpace(*patch.Icon))
	}
	if patch.ProviderMethodConfig != nil {
		set("config", paymentJSONText(*patch.ProviderMethodConfig))
	}
	if patch.Enabled != nil {
		set("enabled", boolInt(*patch.Enabled))
	}
	if patch.SortOrder != nil {
		set("sort_order", *patch.SortOrder)
	}
	if len(parts) == 0 {
		return GetPaymentMethod(ctx, db, id)
	}
	set("updated_at", time.Now().Unix())
	args = append(args, id)
	result, err := db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE payment_methods SET %s WHERE id=?`, strings.Join(parts, ", ")), args...)
	if err != nil {
		if isUniqueIndexErr(err, "idx_payment_methods_channel_name_unique", "payment_methods.channel_id") {
			return nil, ErrPaymentMethodNameExists
		}
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrNotFound
	}
	return GetPaymentMethod(ctx, db, id)
}

func ReorderPaymentMethods(ctx context.Context, db *sql.DB, ids []string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for index, id := range ids {
		if _, err := tx.ExecContext(ctx,
			`UPDATE payment_methods SET sort_order=?, updated_at=? WHERE id=?`, index, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func DeletePaymentMethod(ctx context.Context, db *sql.DB, id string) error {
	result, err := db.ExecContext(ctx, `DELETE FROM payment_methods WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

// PaymentOrder contains immutable purchase snapshots and mutable processing
// state. IDs are suitable for external merchant-order references.
type PaymentOrder struct {
	ID                  string          `json:"id"`
	UserID              string          `json:"user_id,omitempty"`
	UserEmail           string          `json:"user_email"`
	Provider            string          `json:"provider"`
	Environment         string          `json:"environment"`
	ChannelID           string          `json:"channel_id"`
	ChannelName         string          `json:"channel_name"`
	MethodID            string          `json:"method_id"`
	MethodName          string          `json:"method_name"`
	MethodType          string          `json:"method_type"`
	MethodConfig        json.RawMessage `json:"method_config"`
	ProductType         string          `json:"product_type"`
	ProductID           string          `json:"product_id"`
	ProductName         string          `json:"product_name"`
	AmountMinor         int64           `json:"amount_minor"`
	PaidAmountMinor     int64           `json:"paid_amount_minor"`
	TaxAmountMinor      int64           `json:"tax_amount_minor"`
	Currency            string          `json:"currency"`
	ProviderAmountMinor int64           `json:"provider_amount_minor"`
	ProviderCurrency    string          `json:"provider_currency"`
	ConversionRate      string          `json:"conversion_rate,omitempty"`
	Credits             float64         `json:"credits"`
	UserGroupID         string          `json:"user_group_id"`
	BillingCycle        string          `json:"billing_cycle"`
	ProviderOrderID     string          `json:"provider_order_id"`
	ProviderPaymentID   string          `json:"provider_payment_id"`
	CheckoutSessionID   string          `json:"checkout_session_id"`
	CheckoutURL         string          `json:"checkout_url"`
	CheckoutExpiresAt   int64           `json:"checkout_expires_at"`
	LastReconciledAt    int64           `json:"last_reconciled_at"`
	ReconcileError      string          `json:"reconcile_error"`
	Status              string          `json:"status"`
	FailureCode         string          `json:"failure_code"`
	FailureMessage      string          `json:"failure_message"`
	PaidAt              int64           `json:"paid_at"`
	FulfilledAt         int64           `json:"fulfilled_at"`
	CreatedAt           int64           `json:"created_at"`
	UpdatedAt           int64           `json:"updated_at"`
}

type PaymentOrderCreateInput struct {
	UserID          string `json:"user_id"`
	PaymentMethodID string `json:"payment_method_id"`
	ProductType     string `json:"product_type"`
	ProductID       string `json:"product_id"`
	BillingCycle    string `json:"billing_cycle"`
}

type PaymentOrderFilter struct {
	UserID      string
	ChannelID   string
	Provider    string
	Status      string
	ProductType string
	Search      string
	Limit       int
	Offset      int
}

const paymentOrderCols = `id, user_id, user_email, provider, environment, channel_id, channel_name, method_id, method_name, method_type, method_config, product_type, product_id, product_name, amount_minor, paid_amount_minor, tax_amount_minor, currency, provider_amount_minor, provider_currency, conversion_rate, credits, user_group_id, billing_cycle, provider_order_id, provider_payment_id, checkout_session_id, checkout_url, checkout_expires_at, last_reconciled_at, reconcile_error, status, failure_code, failure_message, paid_at, fulfilled_at, created_at, updated_at`

func scanPaymentOrder(s scanner) (PaymentOrder, error) {
	var order PaymentOrder
	var userID sql.NullString
	var methodConfig string
	err := s.Scan(
		&order.ID,
		&userID,
		&order.UserEmail,
		&order.Provider,
		&order.Environment,
		&order.ChannelID,
		&order.ChannelName,
		&order.MethodID,
		&order.MethodName,
		&order.MethodType,
		&methodConfig,
		&order.ProductType,
		&order.ProductID,
		&order.ProductName,
		&order.AmountMinor,
		&order.PaidAmountMinor,
		&order.TaxAmountMinor,
		&order.Currency,
		&order.ProviderAmountMinor,
		&order.ProviderCurrency,
		&order.ConversionRate,
		&order.Credits,
		&order.UserGroupID,
		&order.BillingCycle,
		&order.ProviderOrderID,
		&order.ProviderPaymentID,
		&order.CheckoutSessionID,
		&order.CheckoutURL,
		&order.CheckoutExpiresAt,
		&order.LastReconciledAt,
		&order.ReconcileError,
		&order.Status,
		&order.FailureCode,
		&order.FailureMessage,
		&order.PaidAt,
		&order.FulfilledAt,
		&order.CreatedAt,
		&order.UpdatedAt,
	)
	order.UserID = userID.String
	order.MethodConfig = json.RawMessage(methodConfig)
	if order.ProviderAmountMinor <= 0 {
		order.ProviderAmountMinor = order.AmountMinor
	}
	if strings.TrimSpace(order.ProviderCurrency) == "" {
		order.ProviderCurrency = order.Currency
	}
	return order, err
}

type paymentRowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func paymentOrderByID(ctx context.Context, q paymentRowQueryer, id string, lock bool) (PaymentOrder, error) {
	query := `SELECT ` + paymentOrderCols + ` FROM payment_orders WHERE id=?`
	if lock && usePostgres {
		query += ` FOR UPDATE`
	}
	order, err := scanPaymentOrder(q.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return order, ErrNotFound
	}
	return order, err
}

func CreatePaymentOrder(ctx context.Context, db *sql.DB, input PaymentOrderCreateInput) (*PaymentOrder, error) {
	input.UserID = strings.TrimSpace(input.UserID)
	input.PaymentMethodID = strings.TrimSpace(input.PaymentMethodID)
	input.ProductType = normalizePaymentIdentifier(input.ProductType)
	input.ProductID = strings.TrimSpace(input.ProductID)
	input.BillingCycle = normalizePaymentIdentifier(input.BillingCycle)
	if input.UserID == "" || input.PaymentMethodID == "" || input.ProductID == "" {
		return nil, ErrInvalidPaymentProduct
	}
	orderID, err := newPaymentOrderID()
	if err != nil {
		return nil, fmt.Errorf("generate payment order id: %w", err)
	}
	tx, err := db.BeginTx(ctx, paymentWriteTxOptions())
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	var method EnabledPaymentMethod
	var methodConfig string
	var channelConfig string
	var paymentEnvironment string
	methodQuery := `SELECT pm.id, pm.channel_id, pc.name, pc.provider, pc.environment, pc.config, pm.name, pm.type, pm.icon, pm.config, pm.sort_order
		   FROM payment_methods pm
		   JOIN payment_channels pc ON pc.id=pm.channel_id
		  WHERE pm.id=? AND pm.enabled=1 AND pc.enabled=1`
	if usePostgres {
		methodQuery += ` FOR SHARE OF pm, pc`
	}
	err = tx.QueryRowContext(ctx, methodQuery, input.PaymentMethodID).Scan(
		&method.ID,
		&method.ChannelID,
		&method.ChannelName,
		&method.Provider,
		&paymentEnvironment,
		&channelConfig,
		&method.Name,
		&method.Type,
		&method.Icon,
		&methodConfig,
		&method.SortOrder,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPaymentMethodUnavailable
	}
	if err != nil {
		return nil, err
	}

	order := PaymentOrder{
		ID:           orderID,
		UserID:       input.UserID,
		Provider:     method.Provider,
		Environment:  paymentEnvironment,
		ChannelID:    method.ChannelID,
		ChannelName:  method.ChannelName,
		MethodID:     method.ID,
		MethodName:   method.Name,
		MethodType:   method.Type,
		MethodConfig: json.RawMessage(methodConfig),
		ProductType:  input.ProductType,
		ProductID:    input.ProductID,
		BillingCycle: input.BillingCycle,
		Currency:     paymentSettlementCurrency(ctx, tx),
		Status:       PaymentOrderPending,
	}
	userQuery := `SELECT email, status, role, group_id, group_expires_at, previous_group_id FROM users WHERE id=?`
	if usePostgres {
		userQuery += ` FOR UPDATE`
	}
	var userStatus string
	var userRole string
	var currentUserGroupID string
	var currentUserGroupExpiresAt int64
	var currentUserPreviousGroupID string
	if err := tx.QueryRowContext(ctx, userQuery, input.UserID).Scan(
		&order.UserEmail, &userStatus, &userRole, &currentUserGroupID, &currentUserGroupExpiresAt, &currentUserPreviousGroupID,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if userStatus != "active" {
		return nil, ErrPaymentUserUnavailable
	}
	if order.Environment == PaymentEnvironmentTest && userRole != "admin" {
		return nil, ErrPaymentMethodUnavailable
	}

	switch input.ProductType {
	case PaymentProductCreditPackage:
		if input.BillingCycle != "" {
			return nil, ErrInvalidPaymentProduct
		}
		err = tx.QueryRowContext(ctx,
			`SELECT name, credits, price_amount_minor
			   FROM credit_packages
			  WHERE id=? AND enabled=1 AND credits>0 AND price_amount_minor>0`, input.ProductID,
		).Scan(&order.ProductName, &order.Credits, &order.AmountMinor)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPaymentProductUnavailable
		}
		if err != nil {
			return nil, err
		}
	case PaymentProductUserGroup:
		if input.BillingCycle != PaymentBillingMonthly && input.BillingCycle != PaymentBillingYearly {
			return nil, ErrInvalidPaymentProduct
		}
		var monthlyPrice, yearlyPrice int64
		var isDefault, isPublic, isPurchasable int
		groupQuery := `SELECT name, monthly_price_amount_minor, yearly_price_amount_minor, is_default, COALESCE(is_public,1), COALESCE(is_purchasable,1)
		   FROM user_groups WHERE id=?`
		if usePostgres {
			groupQuery += ` FOR KEY SHARE`
		}
		err = tx.QueryRowContext(ctx, groupQuery, input.ProductID).Scan(&order.ProductName, &monthlyPrice, &yearlyPrice, &isDefault, &isPublic, &isPurchasable)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPaymentProductUnavailable
		}
		if err != nil {
			return nil, err
		}
		if isDefault != 0 || isPublic == 0 {
			return nil, ErrPaymentProductUnavailable
		}
		if isPurchasable == 0 {
			return nil, ErrPaymentUserGroupNotPurchasable
		}
		if input.BillingCycle == PaymentBillingMonthly {
			order.AmountMinor = monthlyPrice
		} else {
			order.AmountMinor = yearlyPrice
		}
		if order.AmountMinor <= 0 {
			return nil, ErrPaymentProductUnavailable
		}
		if (currentUserGroupID == input.ProductID && currentUserGroupExpiresAt == 0) ||
			(currentUserGroupExpiresAt > 0 && currentUserPreviousGroupID == input.ProductID) {
			return nil, ErrPaymentUserGroupPermanent
		}
		order.UserGroupID = input.ProductID
	default:
		return nil, ErrInvalidPaymentProduct
	}

	order.ProviderAmountMinor = order.AmountMinor
	order.ProviderCurrency = order.Currency
	switch order.Provider {
	case paymentcore.ProviderEPay:
		var cfg paymentcore.EPayConfig
		if json.Unmarshal([]byte(channelConfig), &cfg) != nil {
			return nil, ErrPaymentMethodUnavailable
		}
		providerAmount, providerCurrency, rate, err := paymentcore.EPayProviderAmount(order.AmountMinor, order.Currency, cfg)
		if err != nil {
			return nil, ErrPaymentMethodUnavailable
		}
		order.ProviderAmountMinor = providerAmount
		order.ProviderCurrency = providerCurrency
		order.ConversionRate = rate
	case paymentcore.ProviderWaffo:
		var cfg paymentcore.WaffoConfig
		if json.Unmarshal([]byte(channelConfig), &cfg) != nil {
			return nil, ErrPaymentMethodUnavailable
		}
		providerAmount, providerCurrency, rate, err := paymentcore.WaffoProviderAmount(order.AmountMinor, order.Currency, cfg)
		if err != nil {
			return nil, ErrPaymentMethodUnavailable
		}
		order.ProviderAmountMinor = providerAmount
		order.ProviderCurrency = providerCurrency
		order.ConversionRate = rate
	}

	now := time.Now().Unix()
	order.CreatedAt = now
	order.UpdatedAt = now
	_, err = tx.ExecContext(ctx,
		`INSERT INTO payment_orders(
		   id, user_id, user_email, provider, environment, channel_id, channel_name, method_id, method_name, method_type, method_config,
		   product_type, product_id, product_name, amount_minor, paid_amount_minor, tax_amount_minor, currency,
		   provider_amount_minor, provider_currency, conversion_rate, credits,
		   user_group_id, billing_cycle, provider_order_id, provider_payment_id, checkout_session_id, checkout_url, checkout_expires_at,
		   last_reconciled_at, reconcile_error, status, failure_code,
		   failure_message, paid_at, fulfilled_at, created_at, updated_at
		 ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, ?, ?, ?, ?, ?, '', '', '', '', 0, 0, '', ?, '', '', 0, 0, ?, ?)`,
		order.ID, order.UserID, order.UserEmail, order.Provider, order.Environment, order.ChannelID, order.ChannelName, order.MethodID, order.MethodName, order.MethodType,
		string(order.MethodConfig),
		order.ProductType, order.ProductID, order.ProductName, order.AmountMinor, order.Currency,
		order.ProviderAmountMinor, order.ProviderCurrency, order.ConversionRate, order.Credits,
		order.UserGroupID, order.BillingCycle, order.Status, now, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &order, nil
}

func GetPaymentOrder(ctx context.Context, db *sql.DB, id string) (*PaymentOrder, error) {
	order, err := paymentOrderByID(ctx, db, id, false)
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func GetPaymentOrderForUser(ctx context.Context, db *sql.DB, id, userID string) (*PaymentOrder, error) {
	order, err := scanPaymentOrder(db.QueryRowContext(ctx,
		`SELECT `+paymentOrderCols+` FROM payment_orders WHERE id=? AND user_id=?`, id, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// PaymentOrderAttempt maps one provider-facing merchant order reference to the
// immutable Aivory purchase order. EPay retries create a new attempt because
// the compatible protocol cannot reopen a provider checkout session reliably.
type PaymentOrderAttempt struct {
	MerchantOrderID string `json:"merchant_order_id"`
	OrderID         string `json:"order_id"`
	Provider        string `json:"provider"`
	ChannelID       string `json:"channel_id"`
	ProviderOrderID string `json:"provider_order_id"`
	Status          string `json:"status"`
	PaidAt          int64  `json:"paid_at"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

const paymentOrderAttemptCols = `merchant_order_id, order_id, provider, channel_id, provider_order_id, status, paid_at, created_at, updated_at`

func scanPaymentOrderAttempt(s scanner) (PaymentOrderAttempt, error) {
	var attempt PaymentOrderAttempt
	err := s.Scan(
		&attempt.MerchantOrderID,
		&attempt.OrderID,
		&attempt.Provider,
		&attempt.ChannelID,
		&attempt.ProviderOrderID,
		&attempt.Status,
		&attempt.PaidAt,
		&attempt.CreatedAt,
		&attempt.UpdatedAt,
	)
	return attempt, err
}

func paymentOrderAttemptByMerchantID(ctx context.Context, q paymentRowQueryer, provider, channelID, merchantOrderID string, lock bool) (PaymentOrderAttempt, error) {
	query := `SELECT ` + paymentOrderAttemptCols + ` FROM payment_order_attempts
		WHERE provider=? AND channel_id=? AND merchant_order_id=?`
	if lock && usePostgres {
		query += ` FOR UPDATE`
	}
	attempt, err := scanPaymentOrderAttempt(q.QueryRowContext(
		ctx, query, normalizePaymentIdentifier(provider), strings.TrimSpace(channelID), strings.TrimSpace(merchantOrderID),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return attempt, ErrNotFound
	}
	return attempt, err
}

func paymentOrderAttemptByID(ctx context.Context, q paymentRowQueryer, merchantOrderID string) (PaymentOrderAttempt, error) {
	attempt, err := scanPaymentOrderAttempt(q.QueryRowContext(ctx,
		`SELECT `+paymentOrderAttemptCols+` FROM payment_order_attempts WHERE merchant_order_id=?`,
		strings.TrimSpace(merchantOrderID),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return attempt, ErrNotFound
	}
	return attempt, err
}

// CreatePaymentOrderAttempt atomically reserves an EPay merchant order number
// only while the parent order can still accept payment. Passing an empty
// merchantOrderID mints a fresh 128-bit reference for a retry submission.
func CreatePaymentOrderAttempt(ctx context.Context, db *sql.DB, orderID, merchantOrderID string) (*PaymentOrderAttempt, error) {
	orderID = strings.TrimSpace(orderID)
	merchantOrderID = strings.TrimSpace(merchantOrderID)
	if orderID == "" {
		return nil, ErrNotFound
	}
	generatedMerchantOrderID := merchantOrderID == ""
	if generatedMerchantOrderID {
		var err error
		merchantOrderID, err = newPaymentOrderAttemptID()
		if err != nil {
			return nil, fmt.Errorf("generate payment attempt id: %w", err)
		}
	}
	now := time.Now().Unix()
	result, err := db.ExecContext(ctx,
		`INSERT INTO payment_order_attempts(
		   merchant_order_id, order_id, provider, channel_id, provider_order_id, status, paid_at, created_at, updated_at
		 )
		 SELECT ?, id, provider, channel_id, '', ?, 0, ?, ?
		   FROM payment_orders
		  WHERE id=? AND provider=? AND status IN (?, ?)`,
		merchantOrderID, PaymentOrderAttemptIssued, now, now, orderID, paymentcore.ProviderEPay,
		PaymentOrderPending, PaymentOrderProcessing,
	)
	if err != nil {
		if !generatedMerchantOrderID {
			if existing, getErr := paymentOrderAttemptByID(ctx, db, merchantOrderID); getErr == nil &&
				existing.OrderID == orderID && existing.Provider == paymentcore.ProviderEPay {
				return &existing, nil
			}
		}
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		order, getErr := GetPaymentOrder(ctx, db, orderID)
		if getErr != nil {
			return nil, getErr
		}
		if order.Provider != paymentcore.ProviderEPay {
			return nil, ErrPaymentEventConflict
		}
		return nil, ErrPaymentOrderNotMutable
	}
	attempt, err := paymentOrderAttemptByID(ctx, db, merchantOrderID)
	if err != nil {
		return nil, err
	}
	return &attempt, nil
}

func GetPaymentOrderAttemptByMerchantID(ctx context.Context, db *sql.DB, provider, channelID, merchantOrderID string) (*PaymentOrderAttempt, error) {
	attempt, err := paymentOrderAttemptByMerchantID(ctx, db, provider, channelID, merchantOrderID, false)
	if err != nil {
		return nil, err
	}
	return &attempt, nil
}

func GetPaymentOrderByProviderID(ctx context.Context, db *sql.DB, provider, channelID, providerOrderID string) (*PaymentOrder, error) {
	order, err := scanPaymentOrder(db.QueryRowContext(ctx,
		`SELECT `+paymentOrderCols+`
		   FROM payment_orders
		  WHERE provider=? AND channel_id=? AND provider_order_id=?`,
		normalizePaymentIdentifier(provider), strings.TrimSpace(channelID), strings.TrimSpace(providerOrderID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func GetPaymentOrderByProviderPaymentID(ctx context.Context, db *sql.DB, provider, channelID, providerPaymentID string) (*PaymentOrder, error) {
	order, err := scanPaymentOrder(db.QueryRowContext(ctx,
		`SELECT `+paymentOrderCols+`
		   FROM payment_orders
		  WHERE provider=? AND channel_id=? AND provider_payment_id=?`,
		normalizePaymentIdentifier(provider), strings.TrimSpace(channelID), strings.TrimSpace(providerPaymentID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &order, nil
}

func paymentOrderFilterSQL(filter PaymentOrderFilter) (string, []any) {
	where := []string{}
	args := []any{}
	add := func(clause string, value any) {
		where = append(where, clause)
		args = append(args, value)
	}
	if filter.UserID != "" {
		add("user_id=?", filter.UserID)
	}
	if filter.ChannelID != "" {
		add("channel_id=?", filter.ChannelID)
	}
	if provider := normalizePaymentIdentifier(filter.Provider); provider != "" {
		add("provider=?", provider)
	}
	if filter.Status != "" {
		add("status=?", filter.Status)
	}
	if filter.ProductType != "" {
		add("product_type=?", filter.ProductType)
	}
	if search := strings.ToLower(strings.TrimSpace(filter.Search)); search != "" {
		pattern := "%" + search + "%"
		where = append(where, `(lower(id) LIKE ? OR lower(provider_order_id) LIKE ? OR lower(provider_payment_id) LIKE ? OR lower(user_email) LIKE ?)`)
		args = append(args, pattern, pattern, pattern, pattern)
	}
	if len(where) == 0 {
		return "", args
	}
	return ` WHERE ` + strings.Join(where, ` AND `), args
}

func ListPaymentOrders(ctx context.Context, db *sql.DB, filter PaymentOrderFilter) ([]PaymentOrder, error) {
	whereSQL, args := paymentOrderFilterSQL(filter)
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	query := `SELECT ` + paymentOrderCols + ` FROM payment_orders` + whereSQL
	query += ` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, filter.Offset)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := []PaymentOrder{}
	for rows.Next() {
		order, err := scanPaymentOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

func CountPaymentOrders(ctx context.Context, db *sql.DB, filter PaymentOrderFilter) (int, error) {
	whereSQL, args := paymentOrderFilterSQL(filter)
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_orders`+whereSQL, args...).Scan(&count)
	return count, err
}

func ListPaymentOrdersForUser(ctx context.Context, db *sql.DB, userID string, limit, offset int) ([]PaymentOrder, error) {
	return ListPaymentOrders(ctx, db, PaymentOrderFilter{UserID: userID, Limit: limit, Offset: offset})
}

func CountPendingPaymentOrdersByChannel(ctx context.Context, db *sql.DB, channelID string) (int, error) {
	var count int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM payment_orders WHERE channel_id=? AND status IN (?, ?)`,
		channelID, PaymentOrderPending, PaymentOrderProcessing).Scan(&count)
	return count, err
}

func HasPendingPaymentOrdersByChannel(ctx context.Context, db *sql.DB, channelID string) (bool, error) {
	count, err := CountPendingPaymentOrdersByChannel(ctx, db, channelID)
	return count > 0, err
}

func hasPendingPaymentOrdersForUserGroup(ctx context.Context, q paymentRowQueryer, groupID string) (bool, error) {
	var exists int
	err := q.QueryRowContext(ctx,
		`SELECT CASE WHEN EXISTS(
		   SELECT 1 FROM payment_orders
		    WHERE user_group_id=? AND status IN (?, ?)
		 ) THEN 1 ELSE 0 END`,
		groupID, PaymentOrderPending, PaymentOrderProcessing).Scan(&exists)
	return exists != 0, err
}

func HasPendingPaymentOrdersForUserGroup(ctx context.Context, db *sql.DB, groupID string) (bool, error) {
	return hasPendingPaymentOrdersForUserGroup(ctx, db, groupID)
}

func hasPendingPaymentOrdersForUser(ctx context.Context, q paymentRowQueryer, userID string) (bool, error) {
	var exists int
	err := q.QueryRowContext(ctx,
		`SELECT CASE WHEN EXISTS(
		   SELECT 1 FROM payment_orders
		    WHERE user_id=? AND status IN (?, ?)
		 ) THEN 1 ELSE 0 END`,
		userID, PaymentOrderPending, PaymentOrderProcessing).Scan(&exists)
	return exists != 0, err
}

func HasPendingPaymentOrdersForUser(ctx context.Context, db *sql.DB, userID string) (bool, error) {
	return hasPendingPaymentOrdersForUser(ctx, db, userID)
}

func MarkPaymentOrderProcessing(ctx context.Context, db *sql.DB, id, providerOrderID string) (*PaymentOrder, error) {
	return MarkPaymentOrderCheckoutStarted(ctx, db, id, providerOrderID, "", 0, "")
}

func MarkPaymentOrderCheckoutStarted(ctx context.Context, db *sql.DB, id, providerOrderID, sessionID string, expiresAt int64, checkoutURL string) (*PaymentOrder, error) {
	providerOrderID = strings.TrimSpace(providerOrderID)
	sessionID = strings.TrimSpace(sessionID)
	storedCheckoutURL := strings.TrimSpace(checkoutURL)
	if expiresAt < 0 {
		expiresAt = 0
	}
	result, err := db.ExecContext(ctx,
		`UPDATE payment_orders
		    SET status=?, provider_order_id=CASE WHEN provider_order_id='' THEN ? ELSE provider_order_id END,
		        checkout_session_id=CASE WHEN checkout_session_id='' THEN ? ELSE checkout_session_id END,
		        checkout_url=CASE WHEN checkout_url='' THEN ? ELSE checkout_url END,
		        checkout_expires_at=CASE WHEN checkout_expires_at=0 THEN ? ELSE checkout_expires_at END,
		        reconcile_error='',
		        updated_at=?
		  WHERE id=? AND status IN (?, ?)
		    AND (?='' OR provider_order_id='' OR provider_order_id=?)
		    AND (?='' OR checkout_session_id='' OR checkout_session_id=?)
		    AND (?='' OR checkout_url='' OR checkout_url=?)`,
		PaymentOrderProcessing, providerOrderID, sessionID, storedCheckoutURL, expiresAt, time.Now().Unix(), id,
		PaymentOrderPending, PaymentOrderProcessing, providerOrderID, providerOrderID, sessionID, sessionID,
		storedCheckoutURL, storedCheckoutURL)
	if err != nil {
		if isUniqueIndexErr(err, "idx_payment_orders_provider_order_unique", "payment_orders.provider_order_id") {
			return nil, ErrPaymentProviderOrderConflict
		}
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		order, getErr := GetPaymentOrder(ctx, db, id)
		if getErr != nil {
			return nil, getErr
		}
		if (order.Status == PaymentOrderPending || order.Status == PaymentOrderProcessing) &&
			providerOrderID != "" && order.ProviderOrderID != "" && order.ProviderOrderID != providerOrderID {
			return order, ErrPaymentProviderOrderMismatch
		}
		if (order.Status == PaymentOrderPending || order.Status == PaymentOrderProcessing) &&
			sessionID != "" && order.CheckoutSessionID != "" && order.CheckoutSessionID != sessionID {
			return order, ErrPaymentProviderOrderMismatch
		}
		if (order.Status == PaymentOrderPending || order.Status == PaymentOrderProcessing) &&
			storedCheckoutURL != "" && order.CheckoutURL != "" && order.CheckoutURL != storedCheckoutURL {
			return order, ErrPaymentProviderOrderMismatch
		}
		return order, ErrPaymentOrderNotMutable
	}
	return GetPaymentOrder(ctx, db, id)
}

// DeletePaymentOrder permanently removes an order and its ON DELETE CASCADE
// attempts/events. In-flight orders are intentionally excluded by the DELETE
// predicate so a concurrent checkout or webhook cannot lose its local audit
// record.
func DeletePaymentOrder(ctx context.Context, db *sql.DB, id string, gatewayFinalAcknowledged bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrNotFound
	}
	manualClosePredicate := `AND NOT (status=? AND failure_code='admin_manual_close')`
	args := []any{
		id, PaymentOrderFulfilled, PaymentOrderFailed, PaymentOrderExpired, PaymentOrderCancelled,
	}
	if gatewayFinalAcknowledged {
		manualClosePredicate = ""
	} else {
		args = append(args, PaymentOrderCancelled)
	}
	result, err := db.ExecContext(ctx,
		`DELETE FROM payment_orders
		  WHERE id=? AND status IN (?, ?, ?, ?) `+manualClosePredicate,
		args...)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected > 0 {
		return nil
	}
	var status, failureCode string
	err = db.QueryRowContext(ctx, `SELECT status, failure_code FROM payment_orders WHERE id=?`, id).Scan(&status, &failureCode)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == PaymentOrderCancelled && failureCode == "admin_manual_close" {
		return ErrPaymentOrderDeleteNeedsAck
	}
	return ErrPaymentOrderNotDeletable
}

// PaymentOrderDeletePolicy mirrors DeletePaymentOrder's atomic predicate for
// administrator UI hints. A locally closed EPay order remains recoverable until
// the administrator explicitly confirms that the gateway can no longer charge.
func PaymentOrderDeletePolicy(order PaymentOrder) (canDelete, requiresGatewayConfirmation bool) {
	switch order.Status {
	case PaymentOrderFulfilled, PaymentOrderFailed, PaymentOrderExpired, PaymentOrderCancelled:
		return true, order.Status == PaymentOrderCancelled && order.FailureCode == "admin_manual_close"
	default:
		return false, false
	}
}

func MarkPaymentOrderFailed(ctx context.Context, db *sql.DB, id, code, message string) (*PaymentOrder, error) {
	_, err := db.ExecContext(ctx,
		`UPDATE payment_orders
		    SET status=?, failure_code=?, failure_message=?, updated_at=?
		  WHERE id=? AND status IN (?, ?)`,
		PaymentOrderFailed, strings.TrimSpace(code), strings.TrimSpace(message), time.Now().Unix(), id,
		PaymentOrderPending, PaymentOrderProcessing)
	if err != nil {
		return nil, err
	}
	return GetPaymentOrder(ctx, db, id)
}

func MarkPaymentOrderExpired(ctx context.Context, db *sql.DB, id, providerOrderID string) (*PaymentOrder, error) {
	providerOrderID = strings.TrimSpace(providerOrderID)
	result, err := db.ExecContext(ctx,
		`UPDATE payment_orders
		    SET status=?, provider_order_id=CASE WHEN provider_order_id='' THEN ? ELSE provider_order_id END,
		        updated_at=?
		  WHERE id=? AND status IN (?, ?)
		    AND (?='' OR provider_order_id='' OR provider_order_id=?)`,
		PaymentOrderExpired, providerOrderID, time.Now().Unix(), id,
		PaymentOrderPending, PaymentOrderProcessing, providerOrderID, providerOrderID)
	if err != nil {
		if isUniqueIndexErr(err, "idx_payment_orders_provider_order_unique", "payment_orders.provider_order_id") {
			return nil, ErrPaymentProviderOrderConflict
		}
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		order, getErr := GetPaymentOrder(ctx, db, id)
		if getErr != nil {
			return nil, getErr
		}
		if (order.Status == PaymentOrderPending || order.Status == PaymentOrderProcessing) &&
			providerOrderID != "" && order.ProviderOrderID != "" && order.ProviderOrderID != providerOrderID {
			return order, ErrPaymentProviderOrderMismatch
		}
		return order, ErrPaymentOrderNotMutable
	}
	return GetPaymentOrder(ctx, db, id)
}

func MarkPaymentOrderCancelled(ctx context.Context, db *sql.DB, id, code, message string) (*PaymentOrder, error) {
	result, err := db.ExecContext(ctx,
		`UPDATE payment_orders
		    SET status=?, failure_code=?, failure_message=?, updated_at=?
		  WHERE id=? AND status IN (?, ?)`,
		PaymentOrderCancelled, strings.TrimSpace(code), strings.TrimSpace(message), time.Now().Unix(), id,
		PaymentOrderPending, PaymentOrderProcessing)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		order, getErr := GetPaymentOrder(ctx, db, id)
		if getErr != nil {
			return nil, getErr
		}
		return order, ErrPaymentOrderNotMutable
	}
	return GetPaymentOrder(ctx, db, id)
}

func MarkPaymentOrderReconciled(ctx context.Context, db *sql.DB, id, reconcileError string) (*PaymentOrder, error) {
	reconcileError = strings.TrimSpace(reconcileError)
	if len(reconcileError) > 500 {
		reconcileError = reconcileError[:500]
	}
	result, err := db.ExecContext(ctx,
		`UPDATE payment_orders SET last_reconciled_at=?, reconcile_error=?, updated_at=? WHERE id=?`,
		time.Now().Unix(), reconcileError, time.Now().Unix(), id)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrNotFound
	}
	return GetPaymentOrder(ctx, db, id)
}

func AnnotateClosedPaymentOrder(ctx context.Context, db *sql.DB, id, code, message string) (*PaymentOrder, error) {
	result, err := db.ExecContext(ctx,
		`UPDATE payment_orders SET failure_code=?, failure_message=?, updated_at=?
		  WHERE id=? AND status IN (?, ?)`,
		strings.TrimSpace(code), strings.TrimSpace(message), time.Now().Unix(), id,
		PaymentOrderExpired, PaymentOrderCancelled)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrPaymentOrderNotMutable
	}
	return GetPaymentOrder(ctx, db, id)
}

func CancelPaymentOrderByAdmin(ctx context.Context, db *sql.DB, id, reason string) (*PaymentOrder, error) {
	reason = strings.TrimSpace(reason)
	tx, err := db.BeginTx(ctx, paymentWriteTxOptions())
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	order, err := paymentOrderByID(ctx, tx, id, true)
	if err != nil {
		return nil, err
	}
	if order.Status != PaymentOrderPending && order.Status != PaymentOrderProcessing {
		return nil, ErrPaymentOrderNotMutable
	}
	now := time.Now().Unix()
	eventID := "admin-manual-close:" + order.ID
	eventRowID := genID("pe")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO payment_events(id, provider, channel_id, event_id, order_id, event_type, created_at, processed_at)
		 VALUES(?, ?, ?, ?, ?, 'admin.manual_close', ?, ?)
		 ON CONFLICT(provider, channel_id, event_id) DO NOTHING`,
		eventRowID, order.Provider, order.ChannelID, eventID, order.ID, now, now); err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE payment_orders
		    SET status=?, failure_code='admin_manual_close', failure_message=?,
		        last_reconciled_at=?, reconcile_error='', updated_at=?
		  WHERE id=? AND status IN (?, ?)`,
		PaymentOrderCancelled, reason, now, now, order.ID, PaymentOrderPending, PaymentOrderProcessing)
	if err != nil {
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, ErrPaymentOrderNotMutable
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return GetPaymentOrder(ctx, db, id)
}

// PaymentEvent is an accepted, provider-verified notification. Only identifiers
// and processing timestamps are retained; raw callbacks and their PII are not.
type PaymentEvent struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	ChannelID   string `json:"channel_id"`
	EventID     string `json:"event_id"`
	OrderID     string `json:"order_id"`
	EventType   string `json:"event_type"`
	CreatedAt   int64  `json:"created_at"`
	ProcessedAt int64  `json:"processed_at"`
}

type PaymentEventInput struct {
	Provider  string `json:"provider"`
	ChannelID string `json:"channel_id"`
	EventID   string `json:"event_id"`
	OrderID   string `json:"order_id"`
	EventType string `json:"event_type"`
}

const paymentEventCols = `id, provider, channel_id, event_id, order_id, event_type, created_at, processed_at`

func scanPaymentEvent(s scanner) (PaymentEvent, error) {
	var event PaymentEvent
	if err := s.Scan(
		&event.ID,
		&event.Provider,
		&event.ChannelID,
		&event.EventID,
		&event.OrderID,
		&event.EventType,
		&event.CreatedAt,
		&event.ProcessedAt,
	); err != nil {
		return event, err
	}
	return event, nil
}

func normalizePaymentEventInput(input PaymentEventInput) (PaymentEventInput, error) {
	input.Provider = normalizePaymentIdentifier(input.Provider)
	input.ChannelID = strings.TrimSpace(input.ChannelID)
	input.EventID = strings.TrimSpace(input.EventID)
	input.OrderID = strings.TrimSpace(input.OrderID)
	input.EventType = strings.TrimSpace(input.EventType)
	if input.Provider == "" || input.ChannelID == "" || input.EventID == "" || input.OrderID == "" {
		return input, ErrInvalidPaymentEvent
	}
	return input, nil
}

func RecordPaymentEvent(ctx context.Context, db *sql.DB, input PaymentEventInput) (*PaymentEvent, bool, error) {
	input, err := normalizePaymentEventInput(input)
	if err != nil {
		return nil, false, err
	}
	order, err := GetPaymentOrder(ctx, db, input.OrderID)
	if err != nil {
		return nil, false, err
	}
	if order.Provider != input.Provider || order.ChannelID != input.ChannelID {
		return nil, false, ErrPaymentEventConflict
	}
	now := time.Now().Unix()
	eventID := genID("pe")
	result, err := db.ExecContext(ctx,
		`INSERT INTO payment_events(id, provider, channel_id, event_id, order_id, event_type, created_at, processed_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, 0)
		 ON CONFLICT(provider, channel_id, event_id) DO NOTHING`,
		eventID, input.Provider, input.ChannelID, input.EventID, input.OrderID, input.EventType, now)
	if err != nil {
		return nil, false, err
	}
	created, _ := result.RowsAffected()
	event, err := getPaymentEventByProviderID(ctx, db, input.Provider, input.ChannelID, input.EventID)
	if err != nil {
		return nil, false, err
	}
	if event.OrderID != input.OrderID {
		return nil, false, ErrPaymentEventConflict
	}
	return event, created == 1, nil
}

func GetPaymentEvent(ctx context.Context, db *sql.DB, id string) (*PaymentEvent, error) {
	event, err := scanPaymentEvent(db.QueryRowContext(ctx,
		`SELECT `+paymentEventCols+` FROM payment_events WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func MarkPaymentEventProcessed(ctx context.Context, db *sql.DB, id string) error {
	result, err := db.ExecContext(ctx,
		`UPDATE payment_events
		    SET processed_at=CASE WHEN processed_at=0 THEN ? ELSE processed_at END
		  WHERE id=?`, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}

func getPaymentEventByProviderID(ctx context.Context, q paymentRowQueryer, provider, channelID, eventID string) (*PaymentEvent, error) {
	event, err := scanPaymentEvent(q.QueryRowContext(ctx,
		`SELECT `+paymentEventCols+`
		   FROM payment_events
		  WHERE provider=? AND channel_id=? AND event_id=?`, provider, channelID, eventID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}

func ListPaymentEventsForOrder(ctx context.Context, db *sql.DB, orderID string) ([]PaymentEvent, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT `+paymentEventCols+` FROM payment_events WHERE order_id=? ORDER BY created_at, id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []PaymentEvent{}
	for rows.Next() {
		event, err := scanPaymentEvent(rows)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

type PaymentFulfillmentInput struct {
	PaymentEventInput
	MerchantOrderID   string `json:"merchant_order_id,omitempty"`
	ProviderOrderID   string `json:"provider_order_id"`
	ProviderPaymentID string `json:"provider_payment_id"`
	AmountMinor       *int64 `json:"amount_minor,omitempty"`
	PaidAmountMinor   *int64 `json:"paid_amount_minor,omitempty"`
	TaxAmountMinor    *int64 `json:"tax_amount_minor,omitempty"`
	Currency          string `json:"currency,omitempty"`
}

func markPaymentOrderAttemptPaid(ctx context.Context, tx *sql.Tx, input PaymentFulfillmentInput, now int64) (*PaymentOrderAttempt, error) {
	if input.MerchantOrderID == "" {
		return nil, nil
	}
	attempt, err := paymentOrderAttemptByMerchantID(
		ctx, tx, input.Provider, input.ChannelID, input.MerchantOrderID, true,
	)
	if err != nil {
		return nil, err
	}
	if attempt.OrderID != input.OrderID {
		return nil, ErrPaymentEventConflict
	}
	if attempt.Status != PaymentOrderAttemptIssued && attempt.Status != PaymentOrderAttemptPaid {
		return nil, ErrInvalidPaymentEvent
	}
	if input.ProviderOrderID != "" && attempt.ProviderOrderID != "" && attempt.ProviderOrderID != input.ProviderOrderID {
		return nil, ErrPaymentProviderOrderMismatch
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE payment_order_attempts
		    SET provider_order_id=CASE WHEN provider_order_id='' AND ?<>'' THEN ? ELSE provider_order_id END,
		        status=?, paid_at=CASE WHEN paid_at=0 THEN ? ELSE paid_at END, updated_at=?
		  WHERE merchant_order_id=? AND provider=? AND channel_id=?
		    AND (?='' OR provider_order_id='' OR provider_order_id=?)`,
		input.ProviderOrderID, input.ProviderOrderID, PaymentOrderAttemptPaid, now, now,
		input.MerchantOrderID, input.Provider, input.ChannelID, input.ProviderOrderID,
		input.ProviderOrderID,
	)
	if err != nil {
		if isUniqueIndexErr(err, "idx_payment_order_attempts_provider_order_unique", "payment_order_attempts.provider_order_id") {
			return nil, ErrPaymentProviderOrderConflict
		}
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return nil, ErrPaymentProviderOrderMismatch
	}
	if attempt.ProviderOrderID == "" {
		attempt.ProviderOrderID = input.ProviderOrderID
	}
	attempt.Status = PaymentOrderAttemptPaid
	if attempt.PaidAt == 0 {
		attempt.PaidAt = now
	}
	attempt.UpdatedAt = now
	return &attempt, nil
}

type PaymentFulfillmentResult struct {
	Order          PaymentOrder `json:"order"`
	Event          PaymentEvent `json:"event"`
	Applied        bool         `json:"applied"`
	DuplicateEvent bool         `json:"duplicate_event"`
}

// FulfillPaymentOrder atomically records a verified provider event, grants the
// snapshotted entitlement, and finalizes the order. It is idempotent both by
// provider event id and by order status.
func FulfillPaymentOrder(ctx context.Context, db *sql.DB, input PaymentFulfillmentInput) (*PaymentFulfillmentResult, error) {
	eventInput, err := normalizePaymentEventInput(input.PaymentEventInput)
	if err != nil {
		return nil, err
	}
	input.PaymentEventInput = eventInput
	input.MerchantOrderID = strings.TrimSpace(input.MerchantOrderID)
	input.ProviderOrderID = strings.TrimSpace(input.ProviderOrderID)
	input.ProviderPaymentID = strings.TrimSpace(input.ProviderPaymentID)
	rawCurrency := strings.TrimSpace(input.Currency)
	input.Currency = normalizePaymentCurrency(rawCurrency, "")
	if rawCurrency != "" && input.Currency == "" {
		return nil, ErrPaymentCurrencyMismatch
	}

	tx, err := db.BeginTx(ctx, paymentWriteTxOptions())
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	if _, err := markPaymentOrderAttemptPaid(ctx, tx, input, now); err != nil {
		return nil, err
	}
	event := PaymentEvent{
		ID:        genID("pe"),
		Provider:  eventInput.Provider,
		ChannelID: eventInput.ChannelID,
		EventID:   eventInput.EventID,
		OrderID:   eventInput.OrderID,
		EventType: eventInput.EventType,
		CreatedAt: now,
	}
	insert, err := tx.ExecContext(ctx,
		`INSERT INTO payment_events(id, provider, channel_id, event_id, order_id, event_type, created_at, processed_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?, 0)
		 ON CONFLICT(provider, channel_id, event_id) DO NOTHING`,
		event.ID, event.Provider, event.ChannelID, event.EventID, event.OrderID, event.EventType, event.CreatedAt)
	if err != nil {
		return nil, err
	}
	inserted, _ := insert.RowsAffected()
	if inserted == 0 {
		existing, err := getPaymentEventByProviderID(ctx, tx, event.Provider, event.ChannelID, event.EventID)
		if err != nil {
			return nil, err
		}
		if existing.OrderID != event.OrderID {
			return nil, ErrPaymentEventConflict
		}
		order, err := paymentOrderByID(ctx, tx, event.OrderID, false)
		if err != nil {
			return nil, err
		}
		if input.MerchantOrderID != "" {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
		}
		return &PaymentFulfillmentResult{
			Order: order, Event: *existing, Applied: false, DuplicateEvent: true,
		}, nil
	}

	order, err := paymentOrderByID(ctx, tx, event.OrderID, true)
	if err != nil {
		return nil, err
	}
	if order.Provider != event.Provider || order.ChannelID != event.ChannelID {
		return nil, ErrPaymentEventConflict
	}
	if input.MerchantOrderID == "" && order.ProviderOrderID != "" && input.ProviderOrderID != "" && order.ProviderOrderID != input.ProviderOrderID {
		return nil, ErrPaymentProviderOrderMismatch
	}
	if input.MerchantOrderID == "" && order.ProviderPaymentID != "" && input.ProviderPaymentID != "" && order.ProviderPaymentID != input.ProviderPaymentID {
		return nil, ErrPaymentProviderOrderMismatch
	}
	if input.AmountMinor != nil && order.AmountMinor != *input.AmountMinor {
		return nil, ErrPaymentAmountMismatch
	}
	if input.Currency != "" && order.Currency != input.Currency {
		return nil, ErrPaymentCurrencyMismatch
	}
	paidAmount := order.AmountMinor
	if input.PaidAmountMinor != nil {
		paidAmount = *input.PaidAmountMinor
	}
	taxAmount := int64(0)
	if input.TaxAmountMinor != nil {
		taxAmount = *input.TaxAmountMinor
	}
	if paidAmount <= 0 || taxAmount < 0 || taxAmount > paidAmount {
		return nil, ErrInvalidPaymentEvent
	}

	if order.Status == PaymentOrderFulfilled {
		if order.ProviderPaymentID == "" && input.ProviderPaymentID != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE payment_orders SET provider_payment_id=?, updated_at=? WHERE id=? AND provider_payment_id=''`,
				input.ProviderPaymentID, now, order.ID); err != nil {
				if isUniqueIndexErr(err, "idx_payment_orders_provider_payment_unique", "payment_orders.provider_payment_id") {
					return nil, ErrPaymentProviderOrderConflict
				}
				return nil, err
			}
			order.ProviderPaymentID = input.ProviderPaymentID
			order.UpdatedAt = now
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE payment_events SET processed_at=? WHERE id=?`, now, event.ID); err != nil {
			return nil, err
		}
		event.ProcessedAt = now
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &PaymentFulfillmentResult{Order: order, Event: event}, nil
	}
	// EPay manual close is local-only. A later verified payment must still grant
	// the purchased entitlement so a buyer is never charged without delivery.
	recoverableManualClose := order.Status == PaymentOrderCancelled && order.FailureCode == "admin_manual_close"
	if order.Status != PaymentOrderPending && order.Status != PaymentOrderProcessing && !recoverableManualClose {
		return nil, ErrPaymentOrderNotFulfillable
	}

	userLock := `SELECT group_id, group_expires_at, previous_group_id FROM users WHERE id=?`
	if usePostgres {
		userLock += ` FOR UPDATE`
	}
	var currentGroup, previousGroup string
	var currentExpiry int64
	if err := tx.QueryRowContext(ctx, userLock, order.UserID).Scan(&currentGroup, &currentExpiry, &previousGroup); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	switch order.ProductType {
	case PaymentProductCreditPackage:
		if order.Credits <= 0 || order.UserGroupID != "" || order.BillingCycle != "" {
			return nil, ErrInvalidPaymentProduct
		}
		result, err := tx.ExecContext(ctx,
			`UPDATE users SET credits_permanent=COALESCE(credits_permanent,0)+? WHERE id=?`,
			order.Credits, order.UserID)
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return nil, ErrNotFound
		}
	case PaymentProductUserGroup:
		if order.UserGroupID == "" ||
			(order.BillingCycle != PaymentBillingMonthly && order.BillingCycle != PaymentBillingYearly) {
			return nil, ErrInvalidPaymentProduct
		}
		groupQuery := `SELECT COUNT(*) FROM user_groups WHERE id=?`
		if usePostgres {
			groupQuery = `SELECT 1 FROM user_groups WHERE id=? FOR KEY SHARE`
		}
		var groupExists int
		if err := tx.QueryRowContext(ctx, groupQuery, order.UserGroupID).Scan(&groupExists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrPaymentProductUnavailable
			}
			return nil, err
		}
		if groupExists == 0 {
			return nil, ErrPaymentProductUnavailable
		}

		effectiveGroup := currentGroup
		effectivePrevious := previousGroup
		effectivePermanent := currentExpiry == 0
		if currentExpiry > 0 && currentExpiry <= now {
			effectiveGroup = previousGroup
			if effectiveGroup == "" {
				effectiveGroup = DefaultGroupID
			}
			effectivePrevious = ""
			// previous_group_id only represents a permanent baseline. Once an
			// active paid window has expired, that baseline is effective again.
			effectivePermanent = true
		}
		base := now
		newPrevious := ""
		if effectiveGroup == order.UserGroupID {
			newPrevious = effectivePrevious
			if effectivePermanent {
				// A permanent current or restored baseline remains permanent even
				// when an older pending order for that same group later succeeds.
				base = 0
			} else if currentExpiry > now {
				base = currentExpiry
			}
		} else if effectivePermanent && effectiveGroup != DefaultGroupID {
			newPrevious = effectiveGroup
		} else if !effectivePermanent && effectivePrevious != "" {
			// Replacing one finite paid tier must not make that tier permanent.
			// Preserve only an older permanent baseline, if one exists.
			newPrevious = effectivePrevious
		}
		newExpiry := int64(0)
		if base > 0 {
			newExpiry, err = paymentBillingExpiry(base, order.BillingCycle)
			if err != nil {
				return nil, err
			}
		}
		result, err := tx.ExecContext(ctx,
			`UPDATE users
			    SET group_id=?, group_expires_at=?, previous_group_id=?, token_ver=token_ver+1
			  WHERE id=?`,
			order.UserGroupID, newExpiry, newPrevious, order.UserID)
		if err != nil {
			return nil, err
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return nil, ErrNotFound
		}
	default:
		return nil, ErrInvalidPaymentProduct
	}

	providerOrderID := order.ProviderOrderID
	if providerOrderID == "" {
		providerOrderID = input.ProviderOrderID
	}
	providerPaymentID := order.ProviderPaymentID
	if providerPaymentID == "" {
		providerPaymentID = input.ProviderPaymentID
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE payment_orders
		    SET provider_order_id=?, provider_payment_id=?, status=?, paid_amount_minor=?, tax_amount_minor=?, failure_code='', failure_message='',
		        paid_at=CASE WHEN paid_at=0 THEN ? ELSE paid_at END,
		        fulfilled_at=?, updated_at=?
		  WHERE id=?
		    AND (status IN (?, ?) OR (status=? AND failure_code='admin_manual_close'))`,
		providerOrderID, providerPaymentID, PaymentOrderFulfilled, paidAmount, taxAmount, now, now, now, order.ID,
		PaymentOrderPending, PaymentOrderProcessing, PaymentOrderCancelled)
	if err != nil {
		if isUniqueIndexErr(err, "idx_payment_orders_provider_order_unique", "payment_orders.provider_order_id") ||
			isUniqueIndexErr(err, "idx_payment_orders_provider_payment_unique", "payment_orders.provider_payment_id") {
			return nil, ErrPaymentProviderOrderConflict
		}
		return nil, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return nil, ErrPaymentOrderNotFulfillable
	}
	if _, err := tx.ExecContext(ctx, `UPDATE payment_events SET processed_at=? WHERE id=?`, now, event.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	order.ProviderOrderID = providerOrderID
	order.ProviderPaymentID = providerPaymentID
	order.Status = PaymentOrderFulfilled
	order.PaidAmountMinor = paidAmount
	order.TaxAmountMinor = taxAmount
	order.FailureCode = ""
	order.FailureMessage = ""
	if order.PaidAt == 0 {
		order.PaidAt = now
	}
	order.FulfilledAt = now
	order.UpdatedAt = now
	event.ProcessedAt = now
	return &PaymentFulfillmentResult{Order: order, Event: event, Applied: true}, nil
}

func paymentWriteTxOptions() *sql.TxOptions {
	if usePostgres {
		return &sql.TxOptions{Isolation: sql.LevelSerializable}
	}
	return nil
}

// paymentAdminWriteTxOptions uses Read Committed for PostgreSQL mutations that
// first lock a channel row and then inspect related orders. Once the row lock
// is acquired, Read Committed makes the subsequent pending-order query see a
// checkout that committed while the administrator was waiting; Serializable
// would turn that ordinary race into a serialization failure without adding a
// stronger guarantee here.
func paymentAdminWriteTxOptions() *sql.TxOptions {
	if usePostgres {
		return &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	return nil
}

func validPaymentEnvironment(environment string) bool {
	return environment == PaymentEnvironmentLive || environment == PaymentEnvironmentTest
}

func backfillPaymentEnvironments(db *sql.DB) error {
	rows, err := db.Query(`SELECT id, provider, config FROM payment_channels`)
	if err != nil {
		return err
	}
	type channelEnvironment struct {
		id          string
		environment string
	}
	channels := []channelEnvironment{}
	for rows.Next() {
		var id, provider, rawConfig string
		if err := rows.Scan(&id, &provider, &rawConfig); err != nil {
			_ = rows.Close()
			return err
		}
		environment := PaymentEnvironmentLive
		var config map[string]any
		if json.Unmarshal([]byte(rawConfig), &config) == nil {
			switch normalizePaymentIdentifier(provider) {
			case "stripe":
				key, _ := config["secret_key"].(string)
				key = strings.ToLower(strings.TrimSpace(key))
				if strings.HasPrefix(key, "sk_test_") || strings.HasPrefix(key, "rk_test_") || strings.HasPrefix(key, "rkcs_test_") {
					environment = PaymentEnvironmentTest
				}
			case "waffo":
				mode, _ := config["mode"].(string)
				if strings.EqualFold(strings.TrimSpace(mode), "test") {
					environment = PaymentEnvironmentTest
				}
			}
		}
		channels = append(channels, channelEnvironment{id: id, environment: environment})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, channel := range channels {
		if _, err := tx.Exec(`UPDATE payment_channels SET environment=? WHERE id=?`, channel.environment, channel.id); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE payment_orders SET environment=? WHERE channel_id=?`, channel.environment, channel.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func newPaymentOrderID() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return "po_" + base64.RawURLEncoding.EncodeToString(entropy[:]), nil
}

func newPaymentOrderAttemptID() (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return "pa_" + base64.RawURLEncoding.EncodeToString(entropy[:]), nil
}

func normalizePaymentIdentifier(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizePaymentCurrency(value, fallback string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) == 3 {
		valid := true
		for i := 0; i < len(value); i++ {
			if value[i] < 'A' || value[i] > 'Z' {
				valid = false
				break
			}
		}
		if valid {
			return value
		}
	}
	return fallback
}

func paymentSettlementCurrency(ctx context.Context, q paymentRowQueryer) string {
	currency := DefaultSettlementCurrency
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='settlement_currency'`).Scan(&raw); err == nil {
		var configured string
		if json.Unmarshal([]byte(raw), &configured) == nil {
			currency = normalizePaymentCurrency(configured, DefaultSettlementCurrency)
		}
	}
	return currency
}

func paymentJSONText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

func paymentBillingExpiry(base int64, cycle string) (int64, error) {
	moment := time.Unix(base, 0).UTC()
	switch cycle {
	case PaymentBillingMonthly:
		return moment.AddDate(0, 1, 0).Unix(), nil
	case PaymentBillingYearly:
		return moment.AddDate(1, 0, 0).Unix(), nil
	default:
		return 0, ErrInvalidPaymentProduct
	}
}

func isForeignKeyConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "foreign key") || strings.Contains(message, "23503")
}
