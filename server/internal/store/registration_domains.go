package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var ErrWorkspaceDomainBound = errors.New("workspace is assigned to an email domain; remove its domain rules first")
var ErrInvalidDomain = errors.New("invalid email domain")

const (
	MaxDomainUserEnrollmentBatch = 500
	MaxRegistrationDomainMatches = 50
)

type RegistrationDomain struct {
	SubscriptionPurchaseDisabled bool     `json:"subscription_purchase_disabled"`
	Domain                       string   `json:"domain"`
	Domains                      []string `json:"domains"`
	WorkspaceID                  string   `json:"workspace_id"`
	WorkspaceName                string   `json:"workspace_name"`
	LockPersonal                 bool     `json:"lock_personal"`
	EmailVerificationRequired    bool     `json:"email_verification_required"`
	InitialGroupID               string   `json:"initial_group_id"`
	InitialGroupName             string   `json:"initial_group_name"`
	Enabled                      bool     `json:"enabled"`
	MemberCount                  int      `json:"member_count"`
}
type DomainUser struct {
	UserID       string `json:"user_id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	LockOverride *bool  `json:"lock_override"`
	Locked       bool   `json:"locked"`
}
type DomainUserCandidate struct {
	UserID                    string `json:"user_id"`
	Name                      string `json:"name"`
	Email                     string `json:"email"`
	Status                    string `json:"status"`
	PersonalConversationCount int    `json:"personal_conversation_count"`
}
type DomainAccess struct {
	SubscriptionPurchaseDisabled bool   `json:"subscription_purchase_disabled"`
	Domain                       string `json:"domain"`
	WorkspaceID                  string `json:"workspace_id"`
	Locked                       bool   `json:"locked"`
}
type DomainPersonalDataStatus struct {
	NeedsAction               bool   `json:"needs_action"`
	Domain                    string `json:"domain"`
	WorkspaceID               string `json:"workspace_id"`
	WorkspaceName             string `json:"workspace_name"`
	PersonalConversationCount int    `json:"personal_conversation_count"`
	CanMigrate                bool   `json:"can_migrate"`
	PromptDismissed           bool   `json:"prompt_dismissed"`
}
type DomainUserRemoval struct {
	WorkspaceID                string
	WorkspaceMembershipRemoved bool
	RevokedMessageIDs          []string
}

func NormalizeRegistrationDomain(value string) (string, error) {
	domain := strings.ToLower(strings.TrimSpace(value))
	if len(domain) > 253 || !strings.Contains(domain, ".") {
		return "", ErrInvalidDomain
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", ErrInvalidDomain
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", ErrInvalidDomain
			}
		}
	}
	return domain, nil
}
func emailDomain(email string) string {
	i := strings.LastIndex(email, "@")
	if i < 0 {
		return ""
	}
	domain, _ := NormalizeRegistrationDomain(email[i+1:])
	return domain
}
func registrationDomainEmailVerificationRequired(ctx context.Context, ex RowExecer, email string) (bool, error) {
	var n int
	err := ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM registration_domains d
		JOIN registration_domain_matches m ON m.rule_domain=d.domain
		WHERE m.domain=? AND d.enabled=1 AND d.email_verification_required=1`, emailDomain(email)).Scan(&n)
	return n > 0, err
}

// Called inside the account-creation transaction, including OAuth creation.
func enrollDomainUser(ctx context.Context, ex RowExecer, userID, email string) error {
	matchedDomain := emailDomain(email)
	var ruleDomain, workspaceID string
	var initialGroupID sql.NullString
	err := ex.QueryRowContext(ctx, `SELECT d.domain,d.workspace_id,d.initial_group_id
		FROM registration_domains d JOIN registration_domain_matches m ON m.rule_domain=d.domain
		WHERE m.domain=? AND d.enabled=1`, matchedDomain).Scan(&ruleDomain, &workspaceID, &initialGroupID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// Serializes against domain changes and workspace teardown in PostgreSQL.
	res, err := ex.ExecContext(ctx, `UPDATE workspaces SET id=id WHERE id=? AND COALESCE(deleting,0)=0`, workspaceID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	res, err = ex.ExecContext(ctx, `INSERT INTO domain_users(user_id,domain)
		SELECT ?,d.domain FROM registration_domains d
		JOIN registration_domain_matches m ON m.rule_domain=d.domain
		WHERE d.domain=? AND d.workspace_id=? AND d.enabled=1 AND m.domain=?`, userID, ruleDomain, workspaceID, matchedDomain)
	if err != nil {
		return err
	}
	n, err = res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	membership, err := ex.ExecContext(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES(?,?,'member') ON CONFLICT(workspace_id,user_id) DO NOTHING`, workspaceID, userID)
	if err != nil {
		return err
	}
	if created, rowsErr := membership.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if created == 1 {
		if _, err := ex.ExecContext(ctx, `UPDATE domain_users SET workspace_membership_created=1 WHERE user_id=? AND domain=?`, userID, ruleDomain); err != nil {
			return err
		}
	}
	if initialGroupID.Valid && strings.TrimSpace(initialGroupID.String) != "" {
		_, err = ex.ExecContext(ctx, `UPDATE users SET group_id=?,group_expires_at=0,previous_group_id='' WHERE id=?`, initialGroupID.String, userID)
	}
	return err
}
func GetDomainAccess(ctx context.Context, ex RowExecer, userID string) (*DomainAccess, error) {
	var a DomainAccess
	err := ex.QueryRowContext(ctx, `SELECT d.domain,d.workspace_id,COALESCE(du.lock_override,d.lock_personal),d.subscription_purchase_disabled
 FROM domain_users du JOIN registration_domains d ON d.domain=du.domain WHERE du.user_id=?`, userID).Scan(&a.Domain, &a.WorkspaceID, &a.Locked, &a.SubscriptionPurchaseDisabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}
func ListRegistrationDomains(ctx context.Context, db *sql.DB) ([]RegistrationDomain, error) {
	rows, err := db.QueryContext(ctx, `SELECT d.domain,d.workspace_id,w.name,d.lock_personal,d.email_verification_required,
	 COALESCE(d.initial_group_id,''),COALESCE(g.name,''),d.enabled,d.subscription_purchase_disabled,
	 (SELECT COUNT(*) FROM domain_users du WHERE du.domain=d.domain)
	 FROM registration_domains d JOIN workspaces w ON w.id=d.workspace_id
	 LEFT JOIN user_groups g ON g.id=d.initial_group_id ORDER BY d.domain`)
	if err != nil {
		return nil, err
	}
	out := []RegistrationDomain{}
	for rows.Next() {
		var d RegistrationDomain
		if err := rows.Scan(&d.Domain, &d.WorkspaceID, &d.WorkspaceName, &d.LockPersonal, &d.EmailVerificationRequired, &d.InitialGroupID, &d.InitialGroupName, &d.Enabled, &d.SubscriptionPurchaseDisabled, &d.MemberCount); err != nil {
			_ = rows.Close()
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	byRule := make(map[string]*RegistrationDomain, len(out))
	for i := range out {
		byRule[out[i].Domain] = &out[i]
	}
	matches, err := db.QueryContext(ctx, `SELECT rule_domain,domain FROM registration_domain_matches ORDER BY rule_domain,domain`)
	if err != nil {
		return nil, err
	}
	defer matches.Close()
	for matches.Next() {
		var ruleDomain, domain string
		if err := matches.Scan(&ruleDomain, &domain); err != nil {
			return nil, err
		}
		if rule := byRule[ruleDomain]; rule != nil {
			rule.Domains = append(rule.Domains, domain)
		}
	}
	if err := matches.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if len(out[i].Domains) == 0 {
			out[i].Domains = []string{out[i].Domain}
		}
	}
	return out, nil
}

func normalizeRegistrationDomainMatches(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > MaxRegistrationDomainMatches {
		return nil, ErrInvalidDomain
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		domain, err := NormalizeRegistrationDomain(value)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[domain]; duplicate {
			continue
		}
		seen[domain] = struct{}{}
		out = append(out, domain)
	}
	if len(out) == 0 {
		return nil, ErrInvalidDomain
	}
	return out, nil
}

// BackfillRegistrationDomainMatches keeps databases and older backups that
// predate multi-domain rules behaviorally identical after upgrade.
func BackfillRegistrationDomainMatches(ctx context.Context, ex RowExecer) error {
	_, err := ex.ExecContext(ctx, `INSERT INTO registration_domain_matches(domain,rule_domain)
		SELECT d.domain,d.domain FROM registration_domains d
		WHERE NOT EXISTS (SELECT 1 FROM registration_domain_matches m WHERE m.rule_domain=d.domain)
		ON CONFLICT(domain) DO NOTHING`)
	return err
}

// The stable rule key and target workspace are immutable. The email domains
// matched by the rule may be replaced without disturbing existing bindings.
func SaveRegistrationDomain(ctx context.Context, db *sql.DB, d RegistrationDomain, create bool) error {
	domain, err := NormalizeRegistrationDomain(d.Domain)
	if err != nil {
		return err
	}
	d.Domain = domain
	replaceMatches := d.Domains != nil
	if create && !replaceMatches {
		d.Domains = []string{domain}
		replaceMatches = true
	}
	var normalizedMatches []string
	if replaceMatches {
		normalizedMatches, err = normalizeRegistrationDomainMatches(d.Domains)
		if err != nil {
			return err
		}
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, d.WorkspaceID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var deleting int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(deleting,0) FROM workspaces WHERE id=?`, d.WorkspaceID).Scan(&deleting); err != nil {
		return err
	}
	if deleting != 0 {
		return ErrNotFound
	}
	var initialGroupID any
	d.InitialGroupID = strings.TrimSpace(d.InitialGroupID)
	if d.InitialGroupID != "" {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_groups WHERE id=?`, d.InitialGroupID).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return ErrNotFound
		}
		initialGroupID = d.InitialGroupID
	}
	if create {
		// A newly created rule always starts running. Stopping enrollment is an
		// explicit administrative action performed by updating an existing rule.
		d.Enabled = true
		_, err = tx.ExecContext(ctx, `INSERT INTO registration_domains(domain,workspace_id,lock_personal,email_verification_required,initial_group_id,enabled,subscription_purchase_disabled) VALUES(?,?,?,?,?,?,?)`, domain, d.WorkspaceID, boolInt(d.LockPersonal), boolInt(d.EmailVerificationRequired), initialGroupID, boolInt(d.Enabled), boolInt(d.SubscriptionPurchaseDisabled))
	} else {
		var res sql.Result
		res, err = tx.ExecContext(ctx, `UPDATE registration_domains SET lock_personal=?,email_verification_required=?,initial_group_id=?,enabled=?,subscription_purchase_disabled=? WHERE domain=? AND workspace_id=?`, boolInt(d.LockPersonal), boolInt(d.EmailVerificationRequired), initialGroupID, boolInt(d.Enabled), boolInt(d.SubscriptionPurchaseDisabled), domain, d.WorkspaceID)
		if err == nil {
			if n, _ := res.RowsAffected(); n != 1 {
				return ErrNotFound
			}
		}
	}
	if err != nil {
		return err
	}
	if replaceMatches {
		if _, err := tx.ExecContext(ctx, `DELETE FROM registration_domain_matches WHERE rule_domain=?`, domain); err != nil {
			return err
		}
		for _, matchedDomain := range normalizedMatches {
			if _, err := tx.ExecContext(ctx, `INSERT INTO registration_domain_matches(domain,rule_domain) VALUES(?,?)`, matchedDomain, domain); err != nil {
				return err
			}
		}
	}
	if err := ensureLockedDomainMembers(ctx, tx, domain); err != nil {
		return err
	}
	return tx.Commit()
}

// A user may have left while unlocked. Reapplying a lock must restore access
// to the assigned workspace rather than strand them outside every space.
func ensureLockedDomainMembers(ctx context.Context, tx *sql.Tx, domain string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role)
	 SELECT d.workspace_id,du.user_id,'member' FROM domain_users du
	 JOIN registration_domains d ON d.domain=du.domain
	 WHERE d.domain=? AND COALESCE(du.lock_override,d.lock_personal)=1
	 ON CONFLICT(workspace_id,user_id) DO NOTHING`, domain)
	return err
}

func ListDomainUsers(ctx context.Context, db *sql.DB, domain string) ([]DomainUser, error) {
	rows, err := db.QueryContext(ctx, `SELECT u.id,u.name,u.email,du.lock_override,COALESCE(du.lock_override,d.lock_personal)
 FROM domain_users du JOIN users u ON u.id=du.user_id JOIN registration_domains d ON d.domain=du.domain WHERE du.domain=? ORDER BY u.email`, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DomainUser{}
	for rows.Next() {
		var u DomainUser
		var override sql.NullBool
		if err := rows.Scan(&u.UserID, &u.Name, &u.Email, &override, &u.Locked); err != nil {
			return nil, err
		}
		if override.Valid {
			u.LockOverride = &override.Bool
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListDomainUserCandidates returns active or pending accounts that an
// administrator may bind manually. Manual assignment intentionally does not
// require an email-domain match.
func ListDomainUserCandidates(ctx context.Context, db *sql.DB, value, search string) ([]DomainUserCandidate, error) {
	domain, err := NormalizeRegistrationDomain(value)
	if err != nil {
		return nil, err
	}
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM registration_domains WHERE domain=?`, domain).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 1 {
		return nil, ErrNotFound
	}
	search = strings.ToLower(strings.TrimSpace(search))
	pattern := "%" + search + "%"
	rows, err := db.QueryContext(ctx, `SELECT u.id,u.name,u.email,u.status,
	 (SELECT COUNT(*) FROM conversations c
	   WHERE c.user_id=u.id AND COALESCE(c.workspace_id,'')='' AND COALESCE(c.inline_source_conv,'')='')
	 FROM users u
	 WHERE u.role<>'admin' AND u.status IN ('active','pending')
	   AND NOT EXISTS (SELECT 1 FROM domain_users du WHERE du.user_id=u.id)
	   AND (?='' OR LOWER(COALESCE(u.name,'')) LIKE ? OR LOWER(u.email) LIKE ?)
	 ORDER BY u.email LIMIT ?`, search, pattern, pattern, MaxDomainUserEnrollmentBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DomainUserCandidate{}
	for rows.Next() {
		var candidate DomainUserCandidate
		if err := rows.Scan(&candidate.UserID, &candidate.Name, &candidate.Email, &candidate.Status, &candidate.PersonalConversationCount); err != nil {
			return nil, err
		}
		out = append(out, candidate)
	}
	return out, rows.Err()
}

// EnrollExistingDomainUsers binds selected historical accounts to a domain
// and its workspace atomically. Their existing system user group is preserved;
// initial_group_id remains a registration-time policy.
func EnrollExistingDomainUsers(ctx context.Context, db *sql.DB, value string, userIDs []string) ([]string, error) {
	domain, err := NormalizeRegistrationDomain(value)
	if err != nil {
		return nil, err
	}
	var workspaceID string
	if err := db.QueryRowContext(ctx, `SELECT workspace_id FROM registration_domains WHERE domain=?`, domain).Scan(&workspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var deleting int
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(deleting,0) FROM workspaces WHERE id=?`, workspaceID).Scan(&deleting); err != nil {
		return nil, err
	}
	if deleting != 0 {
		return nil, ErrNotFound
	}
	seen := make(map[string]struct{}, len(userIDs))
	added := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		userID = strings.TrimSpace(userID)
		if userID == "" {
			return nil, ErrNotFound
		}
		if _, duplicate := seen[userID]; duplicate {
			continue
		}
		seen[userID] = struct{}{}
		var role, status string
		err := tx.QueryRowContext(ctx, `SELECT role,status FROM users WHERE id=?`, userID).Scan(&role, &status)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, err
		}
		if role == "admin" || (status != "active" && status != "pending") {
			return nil, ErrForbidden
		}
		var alreadyBound int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM domain_users WHERE user_id=?`, userID).Scan(&alreadyBound); err != nil {
			return nil, err
		}
		if alreadyBound != 0 {
			return nil, ErrForbidden
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO domain_users(user_id,domain)
		 SELECT ?,domain FROM registration_domains WHERE domain=? AND workspace_id=?`, userID, domain, workspaceID)
		if err != nil {
			return nil, err
		}
		if n, rowsErr := res.RowsAffected(); rowsErr != nil {
			return nil, rowsErr
		} else if n != 1 {
			return nil, ErrNotFound
		}
		membership, err := tx.ExecContext(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role)
		 VALUES(?,?,'member') ON CONFLICT(workspace_id,user_id) DO NOTHING`, workspaceID, userID)
		if err != nil {
			return nil, err
		}
		if created, rowsErr := membership.RowsAffected(); rowsErr != nil {
			return nil, rowsErr
		} else if created == 1 {
			if _, err := tx.ExecContext(ctx, `UPDATE domain_users SET workspace_membership_created=1 WHERE user_id=? AND domain=?`, userID, domain); err != nil {
				return nil, err
			}
		}
		added = append(added, userID)
	}
	if err := ensureLockedDomainMembers(ctx, tx, domain); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return added, nil
}

// GetDomainPersonalDataStatus reports whether a domain-locked account still
// has personal conversations hidden behind the workspace-only boundary.
func GetDomainPersonalDataStatus(ctx context.Context, db *sql.DB, userID string) (DomainPersonalDataStatus, error) {
	var status DomainPersonalDataStatus
	var locked bool
	err := db.QueryRowContext(ctx, `SELECT d.domain,d.workspace_id,w.name,
	 COALESCE(du.personal_data_prompt_dismissed,0),COALESCE(du.lock_override,d.lock_personal)
	 FROM domain_users du
	 JOIN registration_domains d ON d.domain=du.domain
	 JOIN workspaces w ON w.id=d.workspace_id
	 WHERE du.user_id=?`, userID).Scan(
		&status.Domain, &status.WorkspaceID, &status.WorkspaceName, &status.PromptDismissed, &locked,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return status, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations
	 WHERE user_id=? AND COALESCE(workspace_id,'')='' AND COALESCE(inline_source_conv,'')=''`, userID).Scan(&status.PersonalConversationCount); err != nil {
		return status, err
	}
	status.NeedsAction = locked && status.PersonalConversationCount > 0
	if status.NeedsAction {
		workspace, err := GetWorkspaceForMember(ctx, db, status.WorkspaceID, userID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return status, err
		}
		status.CanMigrate = err == nil && workspace != nil && workspace.CanPrivateConversations
	}
	return status, nil
}

func DismissDomainPersonalDataPrompt(ctx context.Context, db *sql.DB, userID string) error {
	res, err := db.ExecContext(ctx, `UPDATE domain_users SET personal_data_prompt_dismissed=1 WHERE user_id=?`, userID)
	if err != nil {
		return err
	}
	if n, rowsErr := res.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if n != 1 {
		return ErrNotFound
	}
	return nil
}

// ListDomainPersonalConversations returns every personal conversation retained
// behind a domain lock, including inline descendants that normal sidebar lists
// intentionally hide. It is used only by the dedicated preservation export.
func ListDomainPersonalConversations(ctx context.Context, db *sql.DB, userID string, limit, offset int) ([]Conversation, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := db.QueryContext(ctx, `SELECT id,user_id,COALESCE(project_id,''),title,provider,model_id,fast,kb_ids,rag_mode,summary_blocks,
		COALESCE(active_leaf_id,''),provider_state,pinned,archived,starred,created_at,updated_at,
		COALESCE(inline_source_conv,''),COALESCE(inline_parent_id,''),COALESCE(inline_quote,''),COALESCE(workspace_id,''),is_public
	 FROM conversations
	 WHERE user_id=? AND COALESCE(workspace_id,'')=''
	 ORDER BY created_at,id LIMIT ? OFFSET ?`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	conversations := []Conversation{}
	for rows.Next() {
		conversation, err := scanConversation(rows)
		if err != nil {
			return nil, err
		}
		conversations = append(conversations, conversation)
	}
	return conversations, rows.Err()
}

// MigrateDomainPersonalConversations moves every personal conversation owned
// by a locked domain user into the assigned workspace as creator-private data.
// Personal project and knowledge-base references cannot cross the workspace
// boundary, so those links are cleared while messages and attachments remain.
func MigrateDomainPersonalConversations(ctx context.Context, db *sql.DB, userID string) (int, error) {
	access, err := GetDomainAccess(ctx, db, userID)
	if err != nil {
		return 0, err
	}
	if access == nil || !access.Locked {
		return 0, ErrForbidden
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, access.WorkspaceID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var allowed int
	err = tx.QueryRowContext(ctx, `SELECT CASE WHEN w.owner_id=? OR `+isAdminRoleSQL("m.role")+` THEN 1 ELSE COALESCE(m.can_private_conversations,0) END
	 FROM domain_users du JOIN registration_domains d ON d.domain=du.domain
	 JOIN workspaces w ON w.id=d.workspace_id
	 LEFT JOIN workspace_members m ON m.workspace_id=w.id AND m.user_id=du.user_id
	 WHERE du.user_id=? AND d.workspace_id=? AND COALESCE(du.lock_override,d.lock_personal)=1
	   AND COALESCE(w.deleting,0)=0 AND (w.owner_id=? OR m.user_id=?)`,
		userID, userID, access.WorkspaceID, userID, userID).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) || allowed != 1 {
		return 0, ErrForbidden
	}
	if err != nil {
		return 0, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conversations
	 WHERE user_id=? AND COALESCE(workspace_id,'')='' AND COALESCE(inline_source_conv,'')=''`, userID).Scan(&count); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE conversations
		 SET workspace_id=?,project_id=NULL,kb_ids='[]',is_public=0
		 WHERE user_id=? AND COALESCE(workspace_id,'')=''`, access.WorkspaceID, userID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
func UpdateDomainUserAccess(ctx context.Context, db *sql.DB, domain, userID string, override *bool) error {
	var value any
	if override != nil {
		value = boolInt(*override)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var workspaceID string
	if err := tx.QueryRowContext(ctx, `SELECT workspace_id FROM registration_domains WHERE domain=?`, domain).Scan(&workspaceID); err != nil {
		return err
	}
	if err := lockWorkspaceMembershipTx(ctx, tx, workspaceID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE domain_users SET lock_override=? WHERE domain=? AND user_id=?`, value, domain, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	if err := ensureLockedDomainMembers(ctx, tx, domain); err != nil {
		return err
	}
	return tx.Commit()
}

// RemoveDomainUser releases the domain binding. Workspace membership is
// removed only when domain enrollment originally created it; memberships that
// predate the binding and the canonical workspace owner are preserved.
func RemoveDomainUser(ctx context.Context, db *sql.DB, value, userID string) (DomainUserRemoval, error) {
	var result DomainUserRemoval
	domain, err := NormalizeRegistrationDomain(value)
	if err != nil {
		return result, err
	}
	if err := db.QueryRowContext(ctx, `SELECT workspace_id FROM registration_domains WHERE domain=?`, domain).Scan(&result.WorkspaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrNotFound
		}
		return result, err
	}
	tx, err := beginWorkspaceMutationTx(ctx, db, result.WorkspaceID)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var membershipCreated bool
	var ownerID string
	if err := tx.QueryRowContext(ctx, `SELECT du.workspace_membership_created,w.owner_id
		FROM domain_users du JOIN registration_domains d ON d.domain=du.domain
		JOIN workspaces w ON w.id=d.workspace_id
		WHERE du.domain=? AND du.user_id=? AND d.workspace_id=?`, domain, userID, result.WorkspaceID).Scan(&membershipCreated, &ownerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, ErrNotFound
		}
		return result, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM domain_users WHERE domain=? AND user_id=?`, domain, userID); err != nil {
		return result, err
	}
	if membershipCreated && userID != ownerID {
		if _, err := tx.ExecContext(ctx, `DELETE FROM conversation_shares
			WHERE user_id=? AND EXISTS (
				SELECT 1 FROM conversations c
				WHERE c.id=conversation_shares.conversation_id AND c.workspace_id=?
			)`, userID, result.WorkspaceID); err != nil {
			return result, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_kb_member_permissions
			WHERE user_id=? AND EXISTS (
				SELECT 1 FROM knowledge_bases k
				WHERE k.id=workspace_kb_member_permissions.kb_id AND k.workspace_id=?
			)`, userID, result.WorkspaceID); err != nil {
			return result, err
		}
		result.RevokedMessageIDs, err = scrubWorkspaceUserStreamingMessagesTx(ctx, tx, result.WorkspaceID, userID)
		if err != nil {
			return result, err
		}
		membership, err := tx.ExecContext(ctx, `DELETE FROM workspace_members
			WHERE workspace_id=? AND user_id=?
			AND NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.id=workspace_members.workspace_id AND w.owner_id=workspace_members.user_id)`, result.WorkspaceID, userID)
		if err != nil {
			return result, err
		}
		if removed, rowsErr := membership.RowsAffected(); rowsErr != nil {
			return result, rowsErr
		} else {
			result.WorkspaceMembershipRemoved = removed == 1
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func DeleteRegistrationDomain(ctx context.Context, db *sql.DB, domain string) error {
	res, err := db.ExecContext(ctx, `DELETE FROM registration_domains WHERE domain=?`, domain)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotFound
	}
	return nil
}

// Domain enrollment stays in the account-creation transaction. The caller's
// status reflects the global policy; the matched rule is checked again here so
// its verification policy and enrollment are resolved within one transaction.
func CreateRegisteredUser(ctx context.Context, db *sql.DB, email, name, hash, status string) (*User, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if status == "active" {
		domainVerificationRequired, err := registrationDomainEmailVerificationRequired(ctx, tx, email)
		if err != nil {
			return nil, err
		}
		if domainVerificationRequired {
			status = "pending"
		}
	}
	id, err := createUserWithState(ctx, tx, email, name, hash, "user", status, true)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return FindUserByID(ctx, db, id)
}
