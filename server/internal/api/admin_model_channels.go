package api

import (
	"errors"
	"net/http"
	"strings"

	"aivory/server/internal/store"
)

type channelHealthPayload struct {
	Channel store.Channel              `json:"channel"`
	Models  []store.ChannelModelHealth `json:"models"`
}

func channelHealthAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	channel, err := store.GetChannel(r.Context(), d.DB, pathParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	models, err := store.ListChannelModelHealth(r.Context(), d.DB, channel.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, channelHealthPayload{Channel: *channel, Models: models})
}

func channelsHealthAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	models, err := store.ListChannelsModelHealth(r.Context(), d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, models)
}

func recoverChannelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	id := pathParam(r, "id")
	if _, err := store.GetChannel(r.Context(), d.DB, id); err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err := store.ResetChannelResult(r.Context(), d.DB, id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	channel, err := store.GetChannel(r.Context(), d.DB, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, channel)
}

func recoverModelChannelAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	modelID, channelID := pathParam(r, "id"), pathParam(r, "channel_id")
	role := "regular"
	if _, err := store.GetModel(r.Context(), d.DB, modelID); err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if _, err := store.GetChannel(r.Context(), d.DB, channelID); err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	bindings, err := store.ListModelChannelBindings(r.Context(), d.DB, modelID, role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	found := false
	for _, binding := range bindings {
		if binding.ChannelID == channelID {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err := store.ResetModelChannelResult(r.Context(), d.DB, modelID, channelID, role); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	bindings, err = store.ListModelChannelBindings(r.Context(), d.DB, modelID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, splitModelBindings(bindings))
}

type modelChannelBindingPayload struct {
	Regular  []store.ModelChannelBinding `json:"regular"`
	Fallback []store.ModelChannelBinding `json:"fallback"`
}

func listModelChannelsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	model, err := store.GetModel(r.Context(), d.DB, pathParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	bindings, err := store.ListModelChannelBindings(r.Context(), d.DB, model.ID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, splitModelBindings(bindings))
}

func splitModelBindings(bindings []store.ModelChannelBinding) modelChannelBindingPayload {
	out := modelChannelBindingPayload{}
	for _, binding := range bindings {
		binding.Role = "regular"
		out.Regular = append(out.Regular, binding)
	}
	return out
}

func replaceModelChannelsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	model, err := store.GetModel(r.Context(), d.DB, pathParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	var payload modelChannelBindingPayload
	if err := decodeJSON(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	if len(payload.Regular) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("at least one regular channel is required"))
		return
	}
	if err := validateModelChannelPolicy(model); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bindings, err := store.ReplaceModelChannelBindings(r.Context(), d.DB, model, payload.Regular, payload.Fallback)
	if err != nil {
		if errors.Is(err, store.ErrUnsupportedChannelModel) || errors.Is(err, store.ErrInvalidModelChannelBinding) {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, splitModelBindings(bindings))
}

func listChannelModelsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	rows, err := store.ListChannelModels(r.Context(), d.DB, pathParam(r, "id"), false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func replaceChannelModelsAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	channelID := pathParam(r, "id")
	channel, err := store.GetChannel(r.Context(), d.DB, channelID)
	if err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	var rows []store.ChannelModel
	if err := decodeJSON(r, &rows); err != nil {
		writeError(w, http.StatusBadRequest, errInvalidInput)
		return
	}
	for i := range rows {
		rows[i].RequestID = strings.TrimSpace(rows[i].RequestID)
		rows[i].Label = strings.TrimSpace(rows[i].Label)
		rows[i].Description = strings.TrimSpace(rows[i].Description)
		rows[i].Kind = strings.ToLower(strings.TrimSpace(rows[i].Kind))
		if rows[i].RequestID == "" {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
		if rows[i].Label == "" {
			rows[i].Label = rows[i].RequestID
		}
		if rows[i].Kind == "" {
			rows[i].Kind = "chat"
		}
		if channel.Type == "typesafe" {
			rows[i].Kind = "decision"
		}
		if rows[i].Kind == "decision" && channel.Type != "typesafe" {
			writeError(w, http.StatusBadRequest, errors.New("decision models require a typesafe channel"))
			return
		}
		if rows[i].Kind != "chat" && rows[i].Kind != "image" && rows[i].Kind != "embedding" && rows[i].Kind != "decision" {
			writeError(w, http.StatusBadRequest, errInvalidInput)
			return
		}
	}
	result, err := store.ReplaceChannelModels(r.Context(), d.DB, channelID, rows)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// listChannelCapabilitiesAdmin returns only channels that advertise the exact
// request_id. The model editor uses this after request_id is entered, so an
// unsupported provider cannot be selected accidentally.
func listChannelCapabilitiesAdmin(d Deps, w http.ResponseWriter, r *http.Request) {
	requestID := strings.TrimSpace(r.URL.Query().Get("request_id"))
	if requestID == "" {
		writeJSON(w, http.StatusOK, []store.Channel{})
		return
	}
	channels, err := store.ListChannels(r.Context(), d.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]store.Channel, 0, len(channels))
	for _, channel := range channels {
		ok, checkErr := store.ChannelSupportsRequestID(r.Context(), d.DB, channel.ID, requestID)
		if checkErr != nil {
			writeError(w, http.StatusInternalServerError, checkErr)
			return
		}
		if ok {
			out = append(out, channel)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
