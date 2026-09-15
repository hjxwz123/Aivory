package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var ErrWorkspaceDomainBound = errors.New("workspace is assigned to an email domain; remove its domain rules first")
var ErrInvalidDomain = errors.New("invalid email domain")

type RegistrationDomain struct {
	Domain                    string `json:"domain"`
	WorkspaceID               string `json:"workspace_id"`
	WorkspaceName             string `json:"workspace_name"`
	LockPersonal              bool   `json:"lock_personal"`
	EmailVerificationRequired bool   `json:"email_verification_required"`
	InitialGroupID            string `json:"initial_group_id"`
	InitialGroupName          string `json:"initial_group_name"`
	Enabled                   bool   `json:"enabled"`
	MemberCount               int    `json:"member_count"`
}
type DomainUser struct {
	UserID       string `json:"user_id"`
	Name         string `json:"name"`
	Email        string `json:"email"`
	LockOverride *bool  `json:"lock_override"`
	Locked       bool   `json:"locked"`
}
type DomainAccess struct {
	Domain      string `json:"domain"`
	WorkspaceID string `json:"workspace_id"`
	Locked      bool   `json:"locked"`
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
	err := ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM registration_domains
		WHERE domain=? AND enabled=1 AND email_verification_required=1`, emailDomain(email)).Scan(&n)
	return n > 0, err
}

// Called inside the account-creation transaction, including OAuth creation.
func enrollDomainUser(ctx context.Context, ex RowExecer, userID, email string) error {
	domain := emailDomain(email)
	var workspaceID string
	var initialGroupID sql.NullString
	err := ex.QueryRowContext(ctx, `SELECT workspace_id,initial_group_id FROM registration_domains WHERE domain=? AND enabled=1`, domain).Scan(&workspaceID, &initialGroupID)
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
 SELECT ?,domain FROM registration_domains WHERE domain=? AND workspace_id=? AND enabled=1`, userID, domain, workspaceID)
	if err != nil {
		return err
	}
	n, err = res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	if _, err = ex.ExecContext(ctx, `INSERT INTO workspace_members(workspace_id,user_id,role) VALUES(?,?,'member') ON CONFLICT(workspace_id,user_id) DO NOTHING`, workspaceID, userID); err != nil {
		return err
	}
	if initialGroupID.Valid && strings.TrimSpace(initialGroupID.String) != "" {
		_, err = ex.ExecContext(ctx, `UPDATE users SET group_id=?,group_expires_at=0,previous_group_id='' WHERE id=?`, initialGroupID.String, userID)
	}
	return err
}
func GetDomainAccess(ctx context.Context, ex RowExecer, userID string) (*DomainAccess, error) {
	var a DomainAccess
	err := ex.QueryRowContext(ctx, `SELECT d.domain,d.workspace_id,COALESCE(du.lock_override,d.lock_personal)
 FROM domain_users du JOIN registration_domains d ON d.domain=du.domain WHERE du.user_id=?`, userID).Scan(&a.Domain, &a.WorkspaceID, &a.Locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}
func ListRegistrationDomains(ctx context.Context, db *sql.DB) ([]RegistrationDomain, error) {
	rows, err := db.QueryContext(ctx, `SELECT d.domain,d.workspace_id,w.name,d.lock_personal,d.email_verification_required,
	 COALESCE(d.initial_group_id,''),COALESCE(g.name,''),d.enabled,
	 (SELECT COUNT(*) FROM domain_users du WHERE du.domain=d.domain)
	 FROM registration_domains d JOIN workspaces w ON w.id=d.workspace_id
	 LEFT JOIN user_groups g ON g.id=d.initial_group_id ORDER BY d.domain`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RegistrationDomain{}
	for rows.Next() {
		var d RegistrationDomain
		if err := rows.Scan(&d.Domain, &d.WorkspaceID, &d.WorkspaceName, &d.LockPersonal, &d.EmailVerificationRequired, &d.InitialGroupID, &d.InitialGroupName, &d.Enabled, &d.MemberCount); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Domain and target are immutable: changing a target must not silently move existing data/members.
func SaveRegistrationDomain(ctx context.Context, db *sql.DB, d RegistrationDomain, create bool) error {
	domain, err := NormalizeRegistrationDomain(d.Domain)
	if err != nil {
		return err
	}
	d.Domain = domain
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
		_, err = tx.ExecContext(ctx, `INSERT INTO registration_domains(domain,workspace_id,lock_personal,email_verification_required,initial_group_id,enabled) VALUES(?,?,?,?,?,?)`, domain, d.WorkspaceID, boolInt(d.LockPersonal), boolInt(d.EmailVerificationRequired), initialGroupID, boolInt(d.Enabled))
	} else {
		var res sql.Result
		res, err = tx.ExecContext(ctx, `UPDATE registration_domains SET lock_personal=?,email_verification_required=?,initial_group_id=?,enabled=? WHERE domain=? AND workspace_id=?`, boolInt(d.LockPersonal), boolInt(d.EmailVerificationRequired), initialGroupID, boolInt(d.Enabled), domain, d.WorkspaceID)
		if err == nil {
			if n, _ := res.RowsAffected(); n != 1 {
				return ErrNotFound
			}
		}
	}
	if err != nil {
		return err
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
