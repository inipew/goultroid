package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/jobs"
	"github.com/inipew/goultroid/internal/tasks"
)

// ResourceStore is the durable JobDefinition store used by the redesigned
// runtime. It extends Store with explicit resource-profile persistence while
// preserving the existing occurrence/attempt/schedule protocol through
// embedding.
type ResourceStore struct {
	*Store
}

// NewResourceStore returns the production store with explicit JobDefinition
// resource persistence enabled.
func NewResourceStore(db *sql.DB) *ResourceStore {
	return &ResourceStore{Store: NewStore(db)}
}

func encodeResources(resources []tasks.ResourceRequirement) (string, error) {
	seen := make(map[string]struct{}, len(resources))
	for _, requirement := range resources {
		if requirement.Name == "" || requirement.Name != strings.TrimSpace(requirement.Name) || requirement.Amount <= 0 {
			return "", errors.New("job resources require a trimmed name and positive amount")
		}
		if _, duplicate := seen[requirement.Name]; duplicate {
			return "", fmt.Errorf("duplicate job resource %q", requirement.Name)
		}
		seen[requirement.Name] = struct{}{}
	}
	if len(resources) == 0 {
		return "[]", nil
	}
	encoded, err := json.Marshal(resources)
	if err != nil {
		return "", fmt.Errorf("encode job resources: %w", err)
	}
	return string(encoded), nil
}

func decodeResources(jobID, raw string) ([]tasks.ResourceRequirement, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil, nil
	}
	var resources []tasks.ResourceRequirement
	if err := json.Unmarshal([]byte(raw), &resources); err != nil {
		return nil, fmt.Errorf("decode resources for %s: %w", jobID, err)
	}
	if _, err := encodeResources(resources); err != nil {
		return nil, fmt.Errorf("validate resources for %s: %w", jobID, err)
	}
	return append([]tasks.ResourceRequirement(nil), resources...), nil
}

// ListDefinitions restores durable definitions including their explicit
// resource profiles. There is intentionally no pool-based inference here.
func (s *ResourceStore) ListDefinitions(ctx context.Context) ([]jobs.JobDefinition, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, scope_owner, quota_owner, handler_type, version, payload,
		       pool, class, timeout_ms, resources, retry_policy, enabled, revision
		FROM job_definitions ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list job definitions: %w", err)
	}
	defer rows.Close()

	var definitions []jobs.JobDefinition
	for rows.Next() {
		var definition jobs.JobDefinition
		var timeoutMS int64
		var resourcesJSON, retryJSON string
		var enabled int
		if err := rows.Scan(&definition.ID, &definition.ScopeOwner, &definition.QuotaOwner,
			&definition.HandlerType, &definition.Version, &definition.Payload, &definition.Pool,
			&definition.Class, &timeoutMS, &resourcesJSON, &retryJSON, &enabled, &definition.Revision); err != nil {
			return nil, fmt.Errorf("scan job definition: %w", err)
		}
		definition.Timeout = time.Duration(timeoutMS) * time.Millisecond
		definition.Enabled = enabled != 0
		definition.Resources, err = decodeResources(definition.ID, resourcesJSON)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(retryJSON) != "" {
			if err := json.Unmarshal([]byte(retryJSON), &definition.RetryPolicy); err != nil {
				return nil, fmt.Errorf("decode retry policy for %s: %w", definition.ID, err)
			}
		}
		definition.Payload = append([]byte(nil), definition.Payload...)
		definitions = append(definitions, definition)
	}
	return definitions, rows.Err()
}

// SaveDefinition persists the definition and resource profile in the same
// statement/transaction boundary.
func (s *ResourceStore) SaveDefinition(ctx context.Context, def *jobs.JobDefinition) error {
	if def == nil {
		return errors.New("job definition is nil")
	}
	resourcesJSON, err := encodeResources(def.Resources)
	if err != nil {
		return err
	}
	retryPolicyBytes, err := json.Marshal(def.RetryPolicy)
	if err != nil {
		return fmt.Errorf("encode retry policy: %w", err)
	}
	now := time.Now().UTC()
	timeoutMS := def.Timeout.Milliseconds()
	enabledInt := 0
	if def.Enabled {
		enabledInt = 1
	}
	query := `
	INSERT INTO job_definitions (
		id, scope_owner, quota_owner, handler_type, version, payload,
		pool, class, timeout_ms, resources, retry_policy, enabled, revision, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		scope_owner = excluded.scope_owner,
		quota_owner = excluded.quota_owner,
		handler_type = excluded.handler_type,
		version = excluded.version,
		payload = excluded.payload,
		pool = excluded.pool,
		class = excluded.class,
		timeout_ms = excluded.timeout_ms,
		resources = excluded.resources,
		retry_policy = excluded.retry_policy,
		enabled = excluded.enabled,
		revision = revision + 1,
		updated_at = excluded.updated_at;
	`
	if _, err := s.db.ExecContext(ctx, query,
		def.ID, def.ScopeOwner, def.QuotaOwner, def.HandlerType, def.Version, def.Payload,
		def.Pool, def.Class, timeoutMS, resourcesJSON, string(retryPolicyBytes), enabledInt, def.Revision, now,
	); err != nil {
		return fmt.Errorf("failed to save job definition %s: %w", def.ID, err)
	}
	return nil
}

// UpdateDefinitionCAS atomically updates both the resource profile and all
// other definition fields under the existing revision fence.
func (s *ResourceStore) UpdateDefinitionCAS(ctx context.Context, def *jobs.JobDefinition, expectedRevision uint64) error {
	if def == nil {
		return errors.New("job definition is nil")
	}
	resourcesJSON, err := encodeResources(def.Resources)
	if err != nil {
		return err
	}
	retryPolicyBytes, err := json.Marshal(def.RetryPolicy)
	if err != nil {
		return fmt.Errorf("encode retry policy: %w", err)
	}
	now := time.Now().UTC()
	timeoutMS := def.Timeout.Milliseconds()
	enabledInt := 0
	if def.Enabled {
		enabledInt = 1
	}
	query := `
	UPDATE job_definitions SET
		scope_owner = ?, quota_owner = ?, handler_type = ?, version = ?,
		payload = ?, pool = ?, class = ?, timeout_ms = ?, resources = ?,
		retry_policy = ?, enabled = ?,
		revision = revision + 1, updated_at = ?
	WHERE id = ? AND revision = ?;
	`
	res, err := s.db.ExecContext(ctx, query,
		def.ScopeOwner, def.QuotaOwner, def.HandlerType, def.Version,
		def.Payload, def.Pool, def.Class, timeoutMS, resourcesJSON,
		string(retryPolicyBytes), enabledInt, now,
		def.ID, expectedRevision,
	)
	if err != nil {
		return fmt.Errorf("failed to update job definition %s: %w", def.ID, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revision CAS rows affected: %w", err)
	}
	if affected == 0 {
		if _, getErr := s.GetDefinition(ctx, def.ID); getErr != nil {
			return getErr
		}
		return fmt.Errorf("%w: job definition %s", ErrRevisionConflict, def.ID)
	}
	def.Revision = expectedRevision + 1
	return nil
}

// GetDefinition loads one definition including its explicit resource profile.
func (s *ResourceStore) GetDefinition(ctx context.Context, id string) (*jobs.JobDefinition, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, scope_owner, quota_owner, handler_type, version, payload,
		       pool, class, timeout_ms, resources, retry_policy, enabled, revision
		FROM job_definitions WHERE id = ?`, id)
	var def jobs.JobDefinition
	var timeoutMS int64
	var resourcesJSON, retryPolicyJSON string
	var enabledInt int
	if err := row.Scan(
		&def.ID, &def.ScopeOwner, &def.QuotaOwner, &def.HandlerType, &def.Version, &def.Payload,
		&def.Pool, &def.Class, &timeoutMS, &resourcesJSON, &retryPolicyJSON, &enabledInt, &def.Revision,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrDefinitionNotFound
		}
		return nil, fmt.Errorf("failed to query job definition %s: %w", id, err)
	}
	def.Timeout = time.Duration(timeoutMS) * time.Millisecond
	def.Enabled = enabledInt == 1
	var err error
	def.Resources, err = decodeResources(def.ID, resourcesJSON)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(retryPolicyJSON) != "" {
		if err := json.Unmarshal([]byte(retryPolicyJSON), &def.RetryPolicy); err != nil {
			return nil, fmt.Errorf("decode retry policy: %w", err)
		}
	}
	def.Payload = append([]byte(nil), def.Payload...)
	return &def, nil
}
