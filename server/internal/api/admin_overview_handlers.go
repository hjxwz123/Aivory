package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

type adminOverviewHealth struct {
	ChannelReady       bool   `json:"channel_ready"`
	DefaultModelReady  bool   `json:"default_model_ready"`
	TaskModelInherited bool   `json:"task_model_inherited"`
	TaskModelReady     bool   `json:"task_model_ready"`
	EmailVerification  bool   `json:"email_verification"`
	SMTPReady          bool   `json:"smtp_ready"`
	EmailReady         bool   `json:"email_ready"`
	StorageProvider    string `json:"storage_provider"`
	StorageReady       bool   `json:"storage_ready"`
	PaymentsReady      bool   `json:"payments_ready"`
	AllReady           bool   `json:"all_ready"`
}

type adminOverviewResponse struct {
	ChannelCount        int                 `json:"channel_count"`
	EnabledChannelCount int                 `json:"enabled_channel_count"`
	ModelCount          int                 `json:"model_count"`
	GroupCount          int                 `json:"group_count"`
	PaymentChannelCount int                 `json:"payment_channel_count"`
	PaymentMethodCount  int                 `json:"payment_method_count"`
	UserCount           int                 `json:"user_count"`
	Health              adminOverviewHealth `json:"health"`
	Today               *store.UsageTotals  `json:"today"`
}

func overviewSettingString(db *sql.DB, key string) string {
	raw, err := store.GetSetting(db, key)
	if err != nil {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func overviewSettingBool(db *sql.DB, key string) bool {
	raw, err := store.GetSetting(db, key)
	if err != nil {
		return false
	}
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}

// adminOverviewHandler returns exactly the summary needed by the overview.
// Keeping the aggregation server-side avoids seven authenticated Cloudflare
// round trips and avoids running the full analytics dashboard query just to
// display today's headline totals.
func adminOverviewHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	channels, err := store.ListChannels(ctx, d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	models, err := store.ListModels(ctx, d.DB, "", false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	groups, err := store.ListUserGroups(ctx, d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	paymentChannels, err := store.ListPaymentChannels(ctx, d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	paymentMethods, err := store.ListPaymentMethods(ctx, d.DB, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	userCount, err := store.CountUsers(ctx, d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	enabledChannels := make(map[string]bool, len(channels))
	for _, channel := range channels {
		if channel.Enabled {
			enabledChannels[channel.ID] = true
		}
	}
	usableChatModels := make(map[string]bool)
	for _, model := range models {
		if strings.EqualFold(strings.TrimSpace(model.Kind), "chat") && model.Enabled && enabledChannels[model.ChannelID] {
			usableChatModels[model.ID] = true
		}
	}

	defaultModelID := overviewSettingString(d.DB, "default_model_id")
	taskModelID := overviewSettingString(d.DB, "task_model_id")
	emailVerification := overviewSettingBool(d.DB, "email_verification_required")
	smtpReady := overviewSettingString(d.DB, "smtp_host") != "" && overviewSettingString(d.DB, "smtp_from") != ""
	storageProvider := overviewSettingString(d.DB, "storage_provider")
	storageReady := storageProvider == "local" ||
		(storageProvider == "s3" && overviewSettingString(d.DB, "storage_s3_bucket") != "") ||
		(storageProvider == "aliyun_oss" &&
			overviewSettingString(d.DB, "storage_aliyun_bucket") != "" &&
			overviewSettingString(d.DB, "storage_aliyun_endpoint") != "" &&
			overviewSettingString(d.DB, "storage_aliyun_access_key_id") != "" &&
			overviewSettingString(d.DB, "storage_aliyun_access_key_secret") != "")
	taskModelInherited := taskModelID == ""
	health := adminOverviewHealth{
		ChannelReady:       len(enabledChannels) > 0,
		DefaultModelReady:  defaultModelID != "" && usableChatModels[defaultModelID],
		TaskModelInherited: taskModelInherited,
		TaskModelReady:     taskModelInherited || usableChatModels[taskModelID],
		EmailVerification:  emailVerification,
		SMTPReady:          smtpReady,
		EmailReady:         !emailVerification || smtpReady,
		StorageProvider:    storageProvider,
		StorageReady:       storageReady,
		PaymentsReady:      len(paymentChannels) == 0 || len(paymentMethods) > 0,
	}
	health.AllReady = health.ChannelReady && health.DefaultModelReady && health.TaskModelReady &&
		health.EmailReady && health.StorageReady && health.PaymentsReady

	response := adminOverviewResponse{
		ChannelCount:        len(channels),
		EnabledChannelCount: len(enabledChannels),
		ModelCount:          len(models),
		GroupCount:          len(groups),
		PaymentChannelCount: len(paymentChannels),
		PaymentMethodCount:  len(paymentMethods),
		UserCount:           userCount,
		Health:              health,
	}
	if health.AllReady {
		if totals, totalsErr := store.AdminUsageTotals(ctx, d.DB, 1); totalsErr == nil {
			response.Today = &totals
		} else if d.Logger != nil {
			d.Logger.Printf("admin overview totals: %v", totalsErr)
		}
	}
	writeJSON(w, http.StatusOK, response)
}
