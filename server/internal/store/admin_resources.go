package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const (
	adminResourceDefaultLimit = 50
	adminResourceMaxLimit     = 200
)

// AdminResourceFilter is shared by the administrator knowledge-base and
// project inventories. Search targets the resource name; User targets the
// creator's email or display name.
type AdminResourceFilter struct {
	Search string
	User   string
}

// AdminKnowledgeBaseResource is the administrator inventory projection of one
// standalone knowledge base. Project-owned libraries are represented through
// AdminProjectResource instead, so they do not appear twice in the UI.
type AdminKnowledgeBaseResource struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	CreatorID        string `json:"creator_id"`
	CreatorName      string `json:"creator_name"`
	CreatorEmail     string `json:"creator_email"`
	CreatorAvatarURL string `json:"creator_avatar_url,omitempty"`

	WorkspaceID         string `json:"workspace_id,omitempty"`
	WorkspaceName       string `json:"workspace_name,omitempty"`
	WorkspaceOwnerID    string `json:"workspace_owner_id,omitempty"`
	WorkspaceOwnerName  string `json:"workspace_owner_name,omitempty"`
	WorkspaceOwnerEmail string `json:"workspace_owner_email,omitempty"`

	EmbeddingModelID      string `json:"embedding_model_id"`
	EmbeddingModelLabel   string `json:"embedding_model_label"`
	EmbeddingModelEnabled bool   `json:"embedding_model_enabled"`
	EmbeddingDim          int    `json:"embedding_dim"`

	DocumentCount           int   `json:"document_count"`
	ReadyDocumentCount      int   `json:"ready_document_count"`
	FailedDocumentCount     int   `json:"failed_document_count"`
	ProcessingDocumentCount int   `json:"processing_document_count"`
	TotalSizeBytes          int64 `json:"total_size_bytes"`
	ChunkCount              int   `json:"chunk_count"`
	ShareCount              int   `json:"share_count"`
	CreatedAt               int64 `json:"created_at"`
	LastActivityAt          int64 `json:"last_activity_at"`
}

// AdminKnowledgeBaseDetail adds personal-library share membership to the
// inventory row. Workspace knowledge bases use workspace membership instead,
// so Shares is an empty list for those rows.
type AdminKnowledgeBaseDetail struct {
	AdminKnowledgeBaseResource
	Shares []KnowledgeBaseShare `json:"shares"`
}

// AdminProjectResource is the administrator inventory projection of a project
// and its dedicated library. Instructions are selected for detail reads but are
// deliberately omitted from list JSON to keep large prompts out of every row.
type AdminProjectResource struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Instructions     string `json:"-"`
	Accent           string `json:"accent"`
	Emoji            string `json:"emoji"`
	Pinned           bool   `json:"pinned"`
	AutoAddUploads   bool   `json:"auto_add_uploads"`
	CreatorID        string `json:"creator_id"`
	CreatorName      string `json:"creator_name"`
	CreatorEmail     string `json:"creator_email"`
	CreatorAvatarURL string `json:"creator_avatar_url,omitempty"`

	WorkspaceID         string `json:"workspace_id,omitempty"`
	WorkspaceName       string `json:"workspace_name,omitempty"`
	WorkspaceOwnerID    string `json:"workspace_owner_id,omitempty"`
	WorkspaceOwnerName  string `json:"workspace_owner_name,omitempty"`
	WorkspaceOwnerEmail string `json:"workspace_owner_email,omitempty"`

	KBID                  string `json:"kb_id,omitempty"`
	KBName                string `json:"kb_name,omitempty"`
	KBDescription         string `json:"kb_description,omitempty"`
	EmbeddingModelID      string `json:"embedding_model_id,omitempty"`
	EmbeddingModelLabel   string `json:"embedding_model_label,omitempty"`
	EmbeddingModelEnabled bool   `json:"embedding_model_enabled"`
	EmbeddingDim          int    `json:"embedding_dim"`

	DocumentCount             int   `json:"document_count"`
	ReadyDocumentCount        int   `json:"ready_document_count"`
	FailedDocumentCount       int   `json:"failed_document_count"`
	ProcessingDocumentCount   int   `json:"processing_document_count"`
	TotalSizeBytes            int64 `json:"total_size_bytes"`
	ChunkCount                int   `json:"chunk_count"`
	ConversationCount         int   `json:"conversation_count"`
	ActiveConversationCount   int   `json:"active_conversation_count"`
	ArchivedConversationCount int   `json:"archived_conversation_count"`
	CreatedAt                 int64 `json:"created_at"`
	UpdatedAt                 int64 `json:"updated_at"`
	LastActivityAt            int64 `json:"last_activity_at"`
}

// AdminProjectDetail exposes the complete stored project instructions only on
// the single-project endpoint.
type AdminProjectDetail struct {
	AdminProjectResource
	Instructions string `json:"instructions"`
}

// AdminProjectConversation is a compact administrator-facing row for the
// conversations grouped under a project. Full thread/message inspection stays
// on the existing administrator conversation endpoints.
type AdminProjectConversation struct {
	ID               string `json:"id"`
	CreatorID        string `json:"creator_id"`
	CreatorName      string `json:"creator_name"`
	CreatorEmail     string `json:"creator_email"`
	CreatorAvatarURL string `json:"creator_avatar_url,omitempty"`
	Title            string `json:"title"`
	Provider         string `json:"provider"`
	ModelID          string `json:"model_id"`
	ModelLabel       string `json:"model_label"`
	Fast             bool   `json:"fast"`
	Pinned           bool   `json:"pinned"`
	Archived         bool   `json:"archived"`
	Starred          bool   `json:"starred"`
	IsPublic         bool   `json:"is_public"`
	WorkspaceID      string `json:"workspace_id,omitempty"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

const adminDocumentStatsJoin = `
	LEFT JOIN (
		SELECT kb_id,
		       COUNT(*) AS document_count,
		       SUM(CASE WHEN status='ready' THEN 1 ELSE 0 END) AS ready_document_count,
		       SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END) AS failed_document_count,
		       SUM(CASE WHEN status IN ('pending','parsing','embedding') THEN 1 ELSE 0 END) AS processing_document_count,
		       COALESCE(SUM(size_bytes),0) AS total_size_bytes,
		       COALESCE(SUM(chunk_count),0) AS chunk_count,
		       MAX(CASE WHEN ingest_updated_at>0 THEN ingest_updated_at ELSE created_at END) AS last_activity_at
		  FROM documents
		 WHERE COALESCE(kb_id,'')<>''
		 GROUP BY kb_id
	) document_stats ON document_stats.kb_id=resource_kb.id`

const adminKnowledgeBaseSelect = `
	SELECT resource_kb.id, resource_kb.name, resource_kb.description,
	       resource_kb.user_id, COALESCE(creator.name,''), creator.email, COALESCE(creator.settings,''),
	       COALESCE(resource_kb.workspace_id,''), COALESCE(workspace.name,''),
	       COALESCE(workspace.owner_id,''), COALESCE(workspace_owner.name,''), COALESCE(workspace_owner.email,''),
	       resource_kb.embedding_model_id, COALESCE(embedding_model.label,''), COALESCE(embedding_model.enabled,0), resource_kb.embedding_dim,
	       COALESCE(document_stats.document_count,0), COALESCE(document_stats.ready_document_count,0),
	       COALESCE(document_stats.failed_document_count,0), COALESCE(document_stats.processing_document_count,0),
	       COALESCE(document_stats.total_size_bytes,0), COALESCE(document_stats.chunk_count,0),
	       COALESCE(share_stats.share_count,0), resource_kb.created_at,
	       CASE WHEN COALESCE(document_stats.last_activity_at,0)>resource_kb.created_at
	            THEN document_stats.last_activity_at ELSE resource_kb.created_at END
	  FROM knowledge_bases resource_kb
	  JOIN users creator ON creator.id=resource_kb.user_id
	  LEFT JOIN workspaces workspace ON workspace.id=resource_kb.workspace_id
	  LEFT JOIN users workspace_owner ON workspace_owner.id=workspace.owner_id
	  LEFT JOIN models embedding_model ON embedding_model.id=resource_kb.embedding_model_id` + adminDocumentStatsJoin + `
	  LEFT JOIN (
		SELECT kb_id, COUNT(*) AS share_count
		  FROM knowledge_base_shares
		 GROUP BY kb_id
	) share_stats ON share_stats.kb_id=resource_kb.id`

func normalizeAdminResourcePage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = adminResourceDefaultLimit
	}
	if limit > adminResourceMaxLimit {
		limit = adminResourceMaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func adminKnowledgeBaseWhere(filter AdminResourceFilter, id string) (string, []any) {
	conditions := []string{standaloneKnowledgeBasePredicate("resource_kb")}
	args := []any{}
	if id = strings.TrimSpace(id); id != "" {
		conditions = append(conditions, "resource_kb.id=?")
		args = append(args, id)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		conditions = append(conditions, "LOWER(resource_kb.name) LIKE ?")
		args = append(args, "%"+strings.ToLower(search)+"%")
	}
	if user := strings.TrimSpace(filter.User); user != "" {
		like := "%" + strings.ToLower(user) + "%"
		conditions = append(conditions, "(LOWER(creator.email) LIKE ? OR LOWER(creator.name) LIKE ?)")
		args = append(args, like, like)
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func scanAdminKnowledgeBase(s scanner) (AdminKnowledgeBaseResource, error) {
	var item AdminKnowledgeBaseResource
	var creatorSettings string
	var embeddingEnabled int
	err := s.Scan(
		&item.ID, &item.Name, &item.Description,
		&item.CreatorID, &item.CreatorName, &item.CreatorEmail, &creatorSettings,
		&item.WorkspaceID, &item.WorkspaceName, &item.WorkspaceOwnerID, &item.WorkspaceOwnerName, &item.WorkspaceOwnerEmail,
		&item.EmbeddingModelID, &item.EmbeddingModelLabel, &embeddingEnabled, &item.EmbeddingDim,
		&item.DocumentCount, &item.ReadyDocumentCount, &item.FailedDocumentCount, &item.ProcessingDocumentCount,
		&item.TotalSizeBytes, &item.ChunkCount, &item.ShareCount, &item.CreatedAt, &item.LastActivityAt,
	)
	if err != nil {
		return item, err
	}
	item.CreatorAvatarURL = avatarFromSettings(creatorSettings)
	item.EmbeddingModelEnabled = embeddingEnabled != 0
	return item, nil
}

func CountAdminKnowledgeBases(ctx context.Context, db *sql.DB, filter AdminResourceFilter) (int, error) {
	where, args := adminKnowledgeBaseWhere(filter, "")
	var total int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM knowledge_bases resource_kb
		JOIN users creator ON creator.id=resource_kb.user_id`+where, args...).Scan(&total)
	return total, err
}

func ListAdminKnowledgeBases(ctx context.Context, db *sql.DB, filter AdminResourceFilter, limit, offset int) ([]AdminKnowledgeBaseResource, error) {
	limit, offset = normalizeAdminResourcePage(limit, offset)
	where, args := adminKnowledgeBaseWhere(filter, "")
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, adminKnowledgeBaseSelect+where+
		` ORDER BY resource_kb.created_at DESC, resource_kb.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AdminKnowledgeBaseResource{}
	for rows.Next() {
		item, err := scanAdminKnowledgeBase(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func GetAdminKnowledgeBase(ctx context.Context, db *sql.DB, id string) (*AdminKnowledgeBaseDetail, error) {
	if id = strings.TrimSpace(id); id == "" {
		return nil, ErrNotFound
	}
	where, args := adminKnowledgeBaseWhere(AdminResourceFilter{}, id)
	item, err := scanAdminKnowledgeBase(db.QueryRowContext(ctx, adminKnowledgeBaseSelect+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	detail := &AdminKnowledgeBaseDetail{AdminKnowledgeBaseResource: item, Shares: []KnowledgeBaseShare{}}
	if item.WorkspaceID == "" {
		shares, err := ListKnowledgeBaseShares(ctx, db, item.ID, item.CreatorID)
		if err != nil {
			return nil, err
		}
		detail.Shares = shares
	}
	return detail, nil
}

const adminProjectSelect = `
	SELECT project_resource.id, project_resource.name, project_resource.description, project_resource.instructions,
	       project_resource.accent, project_resource.emoji, project_resource.pinned, project_resource.auto_add_uploads,
	       project_resource.user_id, COALESCE(creator.name,''), creator.email, COALESCE(creator.settings,''),
	       COALESCE(project_resource.workspace_id,''), COALESCE(workspace.name,''),
	       COALESCE(workspace.owner_id,''), COALESCE(workspace_owner.name,''), COALESCE(workspace_owner.email,''),
	       COALESCE(project_resource.kb_id,''), COALESCE(resource_kb.name,''), COALESCE(resource_kb.description,''),
	       COALESCE(resource_kb.embedding_model_id,''), COALESCE(embedding_model.label,''), COALESCE(embedding_model.enabled,0),
	       COALESCE(resource_kb.embedding_dim,0),
	       COALESCE(document_stats.document_count,0), COALESCE(document_stats.ready_document_count,0),
	       COALESCE(document_stats.failed_document_count,0), COALESCE(document_stats.processing_document_count,0),
	       COALESCE(document_stats.total_size_bytes,0), COALESCE(document_stats.chunk_count,0),
	       COALESCE(conversation_stats.conversation_count,0), COALESCE(conversation_stats.active_conversation_count,0),
	       COALESCE(conversation_stats.archived_conversation_count,0),
	       project_resource.created_at, project_resource.updated_at,
	       CASE
	         WHEN COALESCE(document_stats.last_activity_at,0)>=project_resource.updated_at
	          AND COALESCE(document_stats.last_activity_at,0)>=COALESCE(conversation_stats.last_activity_at,0)
	           THEN document_stats.last_activity_at
	         WHEN COALESCE(conversation_stats.last_activity_at,0)>=project_resource.updated_at
	           THEN conversation_stats.last_activity_at
	         ELSE project_resource.updated_at
	       END
	  FROM projects project_resource
	  JOIN users creator ON creator.id=project_resource.user_id
	  LEFT JOIN workspaces workspace ON workspace.id=project_resource.workspace_id
	  LEFT JOIN users workspace_owner ON workspace_owner.id=workspace.owner_id
	  LEFT JOIN knowledge_bases resource_kb ON resource_kb.id=project_resource.kb_id
	  LEFT JOIN models embedding_model ON embedding_model.id=resource_kb.embedding_model_id` + adminDocumentStatsJoin + `
	  LEFT JOIN (
		SELECT project_id,
		       COUNT(*) AS conversation_count,
		       SUM(CASE WHEN archived=0 THEN 1 ELSE 0 END) AS active_conversation_count,
		       SUM(CASE WHEN archived=1 THEN 1 ELSE 0 END) AS archived_conversation_count,
		       MAX(updated_at) AS last_activity_at
		  FROM conversations
		 WHERE COALESCE(project_id,'')<>'' AND COALESCE(inline_source_conv,'')=''
		 GROUP BY project_id
	) conversation_stats ON conversation_stats.project_id=project_resource.id`

func adminProjectWhere(filter AdminResourceFilter, id string) (string, []any) {
	conditions := []string{}
	args := []any{}
	if id = strings.TrimSpace(id); id != "" {
		conditions = append(conditions, "project_resource.id=?")
		args = append(args, id)
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		conditions = append(conditions, "LOWER(project_resource.name) LIKE ?")
		args = append(args, "%"+strings.ToLower(search)+"%")
	}
	if user := strings.TrimSpace(filter.User); user != "" {
		like := "%" + strings.ToLower(user) + "%"
		conditions = append(conditions, "(LOWER(creator.email) LIKE ? OR LOWER(creator.name) LIKE ?)")
		args = append(args, like, like)
	}
	if len(conditions) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}

func scanAdminProject(s scanner) (AdminProjectResource, error) {
	var item AdminProjectResource
	var creatorSettings string
	var pinned, autoAddUploads, embeddingEnabled int
	err := s.Scan(
		&item.ID, &item.Name, &item.Description, &item.Instructions,
		&item.Accent, &item.Emoji, &pinned, &autoAddUploads,
		&item.CreatorID, &item.CreatorName, &item.CreatorEmail, &creatorSettings,
		&item.WorkspaceID, &item.WorkspaceName, &item.WorkspaceOwnerID, &item.WorkspaceOwnerName, &item.WorkspaceOwnerEmail,
		&item.KBID, &item.KBName, &item.KBDescription, &item.EmbeddingModelID, &item.EmbeddingModelLabel,
		&embeddingEnabled, &item.EmbeddingDim,
		&item.DocumentCount, &item.ReadyDocumentCount, &item.FailedDocumentCount, &item.ProcessingDocumentCount,
		&item.TotalSizeBytes, &item.ChunkCount,
		&item.ConversationCount, &item.ActiveConversationCount, &item.ArchivedConversationCount,
		&item.CreatedAt, &item.UpdatedAt, &item.LastActivityAt,
	)
	if err != nil {
		return item, err
	}
	item.Pinned = pinned != 0
	item.AutoAddUploads = autoAddUploads != 0
	item.EmbeddingModelEnabled = embeddingEnabled != 0
	item.CreatorAvatarURL = avatarFromSettings(creatorSettings)
	return item, nil
}

func CountAdminProjects(ctx context.Context, db *sql.DB, filter AdminResourceFilter) (int, error) {
	where, args := adminProjectWhere(filter, "")
	var total int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects project_resource
		JOIN users creator ON creator.id=project_resource.user_id`+where, args...).Scan(&total)
	return total, err
}

func ListAdminProjects(ctx context.Context, db *sql.DB, filter AdminResourceFilter, limit, offset int) ([]AdminProjectResource, error) {
	limit, offset = normalizeAdminResourcePage(limit, offset)
	where, args := adminProjectWhere(filter, "")
	args = append(args, limit, offset)
	rows, err := db.QueryContext(ctx, adminProjectSelect+where+
		` ORDER BY project_resource.updated_at DESC, project_resource.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AdminProjectResource{}
	for rows.Next() {
		item, err := scanAdminProject(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func GetAdminProject(ctx context.Context, db *sql.DB, id string) (*AdminProjectDetail, error) {
	if id = strings.TrimSpace(id); id == "" {
		return nil, ErrNotFound
	}
	where, args := adminProjectWhere(AdminResourceFilter{}, id)
	item, err := scanAdminProject(db.QueryRowContext(ctx, adminProjectSelect+where, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &AdminProjectDetail{AdminProjectResource: item, Instructions: item.Instructions}, nil
}

func CountAdminProjectConversations(ctx context.Context, db *sql.DB, projectID string) (int, error) {
	var total int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations
		WHERE project_id=? AND COALESCE(inline_source_conv,'')=''`, strings.TrimSpace(projectID)).Scan(&total)
	return total, err
}

func ListAdminProjectConversations(ctx context.Context, db *sql.DB, projectID string, limit, offset int) ([]AdminProjectConversation, error) {
	limit, offset = normalizeAdminResourcePage(limit, offset)
	rows, err := db.QueryContext(ctx, `
		SELECT conversation.id, conversation.user_id, COALESCE(creator.name,''), COALESCE(creator.email,''), COALESCE(creator.settings,''),
		       conversation.title, conversation.provider, conversation.model_id, COALESCE(model.label,''),
		       conversation.fast, conversation.pinned, conversation.archived, conversation.starred, conversation.is_public,
		       COALESCE(conversation.workspace_id,''), conversation.created_at, conversation.updated_at
		  FROM conversations conversation
		  LEFT JOIN users creator ON creator.id=conversation.user_id
		  LEFT JOIN models model ON model.id=conversation.model_id
		 WHERE conversation.project_id=? AND COALESCE(conversation.inline_source_conv,'')=''
		 ORDER BY conversation.updated_at DESC, conversation.id DESC LIMIT ? OFFSET ?`,
		strings.TrimSpace(projectID), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []AdminProjectConversation{}
	for rows.Next() {
		var item AdminProjectConversation
		var creatorSettings string
		var fast, pinned, archived, starred, public int
		if err := rows.Scan(
			&item.ID, &item.CreatorID, &item.CreatorName, &item.CreatorEmail, &creatorSettings,
			&item.Title, &item.Provider, &item.ModelID, &item.ModelLabel,
			&fast, &pinned, &archived, &starred, &public,
			&item.WorkspaceID, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.CreatorAvatarURL = avatarFromSettings(creatorSettings)
		item.Fast = fast != 0
		item.Pinned = pinned != 0
		item.Archived = archived != 0
		item.Starred = starred != 0
		item.IsPublic = public != 0
		items = append(items, item)
	}
	return items, rows.Err()
}
