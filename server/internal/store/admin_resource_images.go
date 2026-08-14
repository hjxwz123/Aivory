package store

import (
	"context"
	"database/sql"
	"strings"
)

// AdminGeneratedImage is one image artifact enriched with the resource owner,
// source conversation, durable image-usage model, and the original user text.
// Prompt is best-effort: legacy and non-conversational artifacts may not have a
// parent user message.
type AdminGeneratedImage struct {
	ID                string `json:"id"`
	ConversationID    string `json:"conversation_id"`
	ConversationTitle string `json:"conversation_title"`
	MessageID         string `json:"message_id"`
	Filename          string `json:"filename"`
	MimeType          string `json:"mime_type"`
	SizeBytes         int64  `json:"size_bytes"`
	CreatedAt         int64  `json:"created_at"`
	UserID            string `json:"user_id"`
	UserEmail         string `json:"user_email"`
	UserName          string `json:"user_name"`
	WorkspaceID       string `json:"workspace_id"`
	WorkspaceName     string `json:"workspace_name"`
	ModelID           string `json:"model_id"`
	ModelLabel        string `json:"model_label"`
	Prompt            string `json:"prompt"`
	URL               string `json:"url,omitempty"`
}

type AdminGeneratedImageFilter struct {
	UserID  string
	UserQ   string
	ModelID string
}

type AdminGeneratedImageModel struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// billing_usage is the current durable source for the actual image model;
// append-only usage_stats covers generations predating billing_usage. The
// assistant message model is only a final fallback because chat turns can call
// an image model as a tool while retaining the chat model on messages.model_id.
const adminGeneratedImagesBaseQuery = `
SELECT a.id,
       m.conversation_id,
       COALESCE(c.title,''),
       a.message_id,
       a.filename,
       a.mime_type,
       a.size_bytes,
       a.created_at,
       COALESCE(NULLIF(m.author_id,''),c.user_id) AS user_id,
       COALESCE(u.email,'') AS user_email,
       COALESCE(u.name,'') AS user_name,
       COALESCE(c.workspace_id,'') AS workspace_id,
       COALESCE(w.name,'') AS workspace_name,
       COALESCE(NULLIF((
         SELECT bu.model_id
           FROM billing_usage bu
          WHERE bu.message_id=m.id
            AND bu.purpose='image'
            AND bu.images_count>0
          ORDER BY bu.created_at DESC, bu.id DESC
          LIMIT 1
	   ),''),NULLIF((
	     SELECT us.model_id
	       FROM usage_stats us
	      WHERE us.message_id=m.id
	        AND us.purpose='image'
	        AND us.images_count>0
	      ORDER BY us.created_at DESC, us.source_log_id DESC
	      LIMIT 1
	   ),''),NULLIF(m.model_id,''),'') AS model_id,
	   COALESCE(m.model_id,'') AS message_model_id,
       COALESCE(parent.search_text,'') AS prompt,
       COALESCE(m.model_label,'') AS message_model_label
  FROM artifacts a
  JOIN messages m ON m.id=a.message_id
  JOIN conversations c ON c.id=m.conversation_id
  LEFT JOIN users u ON u.id=COALESCE(NULLIF(m.author_id,''),c.user_id)
  LEFT JOIN workspaces w ON w.id=c.workspace_id
  LEFT JOIN messages parent ON parent.id=m.parent_id AND parent.role='user'
 WHERE ` + generatedImageArtifactPredicate

func adminGeneratedImagesWhere(filter AdminGeneratedImageFilter) (string, []any) {
	conds := []string{}
	args := []any{}
	if userID := strings.TrimSpace(filter.UserID); userID != "" {
		conds = append(conds, "t.user_id=?")
		args = append(args, userID)
	} else if q := strings.TrimSpace(filter.UserQ); q != "" {
		like := "%" + strings.ToLower(q) + "%"
		conds = append(conds, "(LOWER(t.user_email) LIKE ? OR LOWER(t.user_name) LIKE ?)")
		args = append(args, like, like)
	}
	if modelID := strings.TrimSpace(filter.ModelID); modelID != "" {
		conds = append(conds, "t.model_id=?")
		args = append(args, modelID)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func scanAdminGeneratedImages(rows *sql.Rows) ([]AdminGeneratedImage, error) {
	out := []AdminGeneratedImage{}
	for rows.Next() {
		var image AdminGeneratedImage
		var messageModelID, messageModelLabel string
		if err := rows.Scan(
			&image.ID,
			&image.ConversationID,
			&image.ConversationTitle,
			&image.MessageID,
			&image.Filename,
			&image.MimeType,
			&image.SizeBytes,
			&image.CreatedAt,
			&image.UserID,
			&image.UserEmail,
			&image.UserName,
			&image.WorkspaceID,
			&image.WorkspaceName,
			&image.ModelID,
			&messageModelID,
			&image.Prompt,
			&messageModelLabel,
			&image.ModelLabel,
		); err != nil {
			return nil, err
		}
		if image.ModelLabel == "" && image.ModelID == messageModelID {
			image.ModelLabel = messageModelLabel
		}
		if image.ModelLabel == "" {
			image.ModelLabel = image.ModelID
		}
		out = append(out, image)
	}
	return out, rows.Err()
}

func ListAdminGeneratedImages(ctx context.Context, db *sql.DB, filter AdminGeneratedImageFilter, limit, offset int) ([]AdminGeneratedImage, error) {
	where, args := adminGeneratedImagesWhere(filter)
	query := `SELECT t.*,COALESCE(model.label,'')
		FROM (` + adminGeneratedImagesBaseQuery + `) t
		LEFT JOIN models model ON model.id=t.model_id` + where + `
		ORDER BY t.created_at DESC,t.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAdminGeneratedImages(rows)
}

func CountAdminGeneratedImages(ctx context.Context, db *sql.DB, filter AdminGeneratedImageFilter) (int, error) {
	where, args := adminGeneratedImagesWhere(filter)
	var total int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (`+adminGeneratedImagesBaseQuery+`) t`+where, args...).Scan(&total)
	return total, err
}

func GetAdminGeneratedImage(ctx context.Context, db *sql.DB, id string) (*AdminGeneratedImage, error) {
	rows, err := db.QueryContext(ctx, `SELECT t.*,COALESCE(model.label,'')
		FROM (`+adminGeneratedImagesBaseQuery+`) t
		LEFT JOIN models model ON model.id=t.model_id
		WHERE t.id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanAdminGeneratedImages(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	return &items[0], nil
}

// ListAdminGeneratedImageModels returns only models that are actually attached
// to an image artifact. This keeps hosted image tools and deleted catalog
// entries filterable instead of assuming every image used a kind=image model.
func ListAdminGeneratedImageModels(ctx context.Context, db *sql.DB) ([]AdminGeneratedImageModel, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT t.model_id,
		COALESCE(NULLIF(model.label,''),
		         CASE WHEN t.model_id=t.message_model_id THEN NULLIF(t.message_model_label,'') ELSE NULL END,
		         t.model_id)
		FROM (`+adminGeneratedImagesBaseQuery+`) t
		LEFT JOIN models model ON model.id=t.model_id
		WHERE t.model_id<>''
		ORDER BY 2 ASC,1 ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminGeneratedImageModel{}
	for rows.Next() {
		var model AdminGeneratedImageModel
		if err := rows.Scan(&model.ID, &model.Label); err != nil {
			return nil, err
		}
		out = append(out, model)
	}
	return out, rows.Err()
}
