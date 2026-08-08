package api

import (
	"bytes"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"aivory/server/internal/store"
)

const (
	userFeedbackScreenshotMaxBytes = 3 * 1024 * 1024
	userFeedbackRequestMaxBytes    = userFeedbackScreenshotMaxBytes + 128*1024
	userFeedbackMaxImageDimension  = 20_000
	userFeedbackMaxImagePixels     = 32_000_000
	userFeedbackMaxPagePathRunes   = 1024
	userFeedbackMaxUserAgentRunes  = 512
	userFeedbackRateLimitMax       = 10
)

var userFeedbackRateLimitWindow = time.Hour

var (
	errUserFeedbackDescriptionRequired  = errors.New("feedback description is required")
	errUserFeedbackMessageRequired      = errors.New("message_id is required")
	errUserFeedbackScreenshotTooLarge   = errors.New("screenshot is too large (max 3 MiB)")
	errUserFeedbackScreenshotFormat     = errors.New("screenshot must be a valid JPEG or PNG image")
	errUserFeedbackScreenshotDimensions = errors.New("screenshot dimensions are invalid or too large")
)

// createUserFeedbackHandler stores a required issue description and optional
// screenshot for a message the authenticated user can access.
func createUserFeedbackHandler(d Deps, w http.ResponseWriter, r *http.Request) {
	u := authUser(r)
	if d.Cache != nil && !rateLimitUser(d, u.ID, "user-feedback", userFeedbackRateLimitMax, userFeedbackRateLimitWindow) {
		writeError(w, http.StatusTooManyRequests, errors.New("feedback submission limit exceeded — try again later"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, userFeedbackRequestMaxBytes)
	if err := r.ParseMultipartForm(userFeedbackRequestMaxBytes); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, errUserFeedbackScreenshotTooLarge)
			return
		}
		writeError(w, http.StatusBadRequest, errors.New("invalid feedback form"))
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}

	description := strings.TrimSpace(r.FormValue("description"))
	if description == "" {
		writeError(w, http.StatusBadRequest, errUserFeedbackDescriptionRequired)
		return
	}
	if !utf8.ValidString(description) || len([]rune(description)) > store.UserFeedbackDescriptionMaxRunes {
		writeError(w, http.StatusBadRequest, errors.New("feedback description must be valid UTF-8 and at most 2000 characters"))
		return
	}
	messageID := strings.TrimSpace(r.FormValue("message_id"))
	if messageID == "" {
		writeError(w, http.StatusBadRequest, errUserFeedbackMessageRequired)
		return
	}
	message, err := store.GetMessage(r.Context(), d.DB, messageID)
	if err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	conversation, err := store.GetConversation(r.Context(), d.DB, message.ConversationID, u.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}

	viewportWidth, err := parseUserFeedbackDimension(r.FormValue("viewport_width"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("viewport_width must be a non-negative integer"))
		return
	}
	viewportHeight, err := parseUserFeedbackDimension(r.FormValue("viewport_height"))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("viewport_height must be a non-negative integer"))
		return
	}
	pagePath, ok := boundedUserFeedbackText(r.FormValue("page_path"), userFeedbackMaxPagePathRunes)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("page_path is too long or invalid"))
		return
	}
	userAgent, _ := boundedUserFeedbackText(r.UserAgent(), userFeedbackMaxUserAgentRunes)

	screenshot, screenshotMIME, screenshotWidth, screenshotHeight, status, err := readUserFeedbackScreenshot(r)
	if err != nil {
		writeError(w, status, err)
		return
	}
	created, err := store.CreateUserFeedback(r.Context(), d.DB, store.UserFeedback{
		UserID:            u.ID,
		MessageID:         message.ID,
		ConversationID:    conversation.ID,
		ConversationTitle: conversation.Title,
		Description:       description,
		PagePath:          pagePath,
		UserAgent:         userAgent,
		ViewportWidth:     viewportWidth,
		ViewportHeight:    viewportHeight,
		Screenshot:        screenshot,
		ScreenshotMIME:    screenshotMIME,
		ScreenshotWidth:   screenshotWidth,
		ScreenshotHeight:  screenshotHeight,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"ok":         true,
		"id":         created.ID,
		"created_at": created.CreatedAt,
	})
}

func parseUserFeedbackDimension(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > 100_000 {
		return 0, errors.New("invalid dimension")
	}
	return value, nil
}

func boundedUserFeedbackText(raw string, maxRunes int) (string, bool) {
	value := strings.TrimSpace(raw)
	if !utf8.ValidString(value) || len([]rune(value)) > maxRunes {
		return "", false
	}
	return value, true
}

func readUserFeedbackScreenshot(r *http.Request) ([]byte, string, int, int, int, error) {
	file, _, err := r.FormFile("screenshot")
	if errors.Is(err, http.ErrMissingFile) {
		return nil, "", 0, 0, http.StatusOK, nil
	}
	if err != nil {
		return nil, "", 0, 0, http.StatusBadRequest, errUserFeedbackScreenshotFormat
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, userFeedbackScreenshotMaxBytes+1))
	if err != nil {
		return nil, "", 0, 0, http.StatusBadRequest, errUserFeedbackScreenshotFormat
	}
	if len(data) > userFeedbackScreenshotMaxBytes {
		return nil, "", 0, 0, http.StatusRequestEntityTooLarge, errUserFeedbackScreenshotTooLarge
	}
	if len(data) == 0 {
		return nil, "", 0, 0, http.StatusBadRequest, errUserFeedbackScreenshotFormat
	}
	mime := http.DetectContentType(data)
	if mime != "image/jpeg" && mime != "image/png" {
		return nil, "", 0, 0, http.StatusUnsupportedMediaType, errUserFeedbackScreenshotFormat
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		return nil, "", 0, 0, http.StatusUnsupportedMediaType, errUserFeedbackScreenshotFormat
	}
	if config.Width <= 0 || config.Height <= 0 ||
		config.Width > userFeedbackMaxImageDimension || config.Height > userFeedbackMaxImageDimension ||
		int64(config.Width)*int64(config.Height) > userFeedbackMaxImagePixels {
		return nil, "", 0, 0, http.StatusBadRequest, errUserFeedbackScreenshotDimensions
	}
	return data, mime, config.Width, config.Height, http.StatusOK, nil
}
