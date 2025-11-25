package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oremus-labs/ol-model-manager/internal/catalog"
	"github.com/oremus-labs/ol-model-manager/internal/vllm"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// JobStatus represents asynchronous job state.
type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobDone      JobStatus = "completed"
	JobFailed    JobStatus = "failed"
	JobCancelled JobStatus = "cancelled"
)

// Job represents an asynchronous task (e.g., weight installation).
type Job struct {
	ID          string                 `json:"id"`
	Type        string                 `json:"type"`
	Status      JobStatus              `json:"status"`
	Stage       string                 `json:"stage,omitempty"`
	Progress    int                    `json:"progress,omitempty"`
	Message     string                 `json:"message,omitempty"`
	Payload     map[string]interface{} `json:"payload,omitempty"`
	Result      map[string]interface{} `json:"result,omitempty"`
	Error       string                 `json:"error,omitempty"`
	Attempt     int                    `json:"attempt,omitempty"`
	MaxAttempts int                    `json:"maxAttempts,omitempty"`
	CancelledAt *time.Time             `json:"cancelledAt,omitempty"`
	Logs        []JobLogEntry          `json:"logs,omitempty"`
	CreatedAt   time.Time              `json:"createdAt"`
	UpdatedAt   time.Time              `json:"updatedAt"`
}

// JobLogEntry captures per-job log lines.
type JobLogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level,omitempty"`
	Stage     string    `json:"stage,omitempty"`
	Message   string    `json:"message"`
}

// HistoryEntry stores past actions (installations, activations, etc.).
type HistoryEntry struct {
	ID        string                 `json:"id"`
	Event     string                 `json:"event"`
	ModelID   string                 `json:"modelId,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt time.Time              `json:"createdAt"`
}

// Notification represents a delivery channel (e.g., Slack webhook).
type Notification struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Target    string            `json:"target"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// NotificationStats captures aggregate delivery health for dashboards.
type NotificationStats struct {
	Channels  int        `json:"channels"`
	Delivered int        `json:"delivered"`
	Failed    int        `json:"failed"`
	Tested    int        `json:"tested"`
	LastEvent *time.Time `json:"lastEvent,omitempty"`
}

// APIToken represents an issued token with optional scopes.
type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	Hash       string     `json:"-"`
}

// Policy stores arbitrary policy documents.
type Policy struct {
	Name      string    `json:"name"`
	Document  string    `json:"document"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// PolicyVersion captures immutable revisions for rollback.
type PolicyVersion struct {
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	Document  string    `json:"document"`
	CreatedAt time.Time `json:"createdAt"`
}

// Backup represents a recorded backup snapshot.
type Backup struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Location  string    `json:"location"`
	Notes     string    `json:"notes,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Playbook represents a curated workflow definition stored in the datastore.
type Playbook struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Tags        []string        `json:"tags,omitempty"`
	Spec        json.RawMessage `json:"spec"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

// Store wraps the persistence database used for jobs + history.
type Store struct {
	db     *sql.DB
	driver string
}

// ErrPlaybookNotFound indicates that the requested playbook does not exist.
var ErrPlaybookNotFound = errors.New("playbook not found")

// Open initializes the datastore using the supplied DSN/file path and driver.
func Open(dsn string, driver string) (*Store, error) {
	if driver == "" {
		driver = "sqlite"
	}
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("datastore DSN is required")
	}

	var (
		db  *sql.DB
		err error
	)

	switch driver {
	case "sqlite":
		if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
			return nil, fmt.Errorf("failed to create datastore directory: %w", err)
		}
		conn := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on", dsn)
		db, err = sql.Open("sqlite", conn)
	case "postgres":
		db, err = sql.Open("pgx", dsn)
	default:
		return nil, fmt.Errorf("unsupported datastore driver: %s", driver)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to open datastore: %w", err)
	}
	if err := initSchema(db, driver); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, driver: driver}, nil
}

func initSchema(db *sql.DB, driver string) error {
	var stmts []string
	if driver == "sqlite" {
		stmts = append(stmts, `PRAGMA journal_mode=WAL;`)
	}
	jobTable := `CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			status TEXT NOT NULL,
			stage TEXT,
			progress INTEGER DEFAULT 0,
			message TEXT,
			payload TEXT,
			result TEXT,
			error TEXT,
			attempt INTEGER DEFAULT 0,
			max_attempts INTEGER DEFAULT 1,
			cancelled_at TIMESTAMP,
			logs TEXT,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`
	historyTable := `CREATE TABLE IF NOT EXISTS history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event TEXT NOT NULL,
			model_id TEXT,
			metadata TEXT,
			created_at TIMESTAMP NOT NULL
		);`
	hfModelsTable := `CREATE TABLE IF NOT EXISTS hf_models (
			model_id TEXT PRIMARY KEY,
			payload TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`
	notificationsTable := `CREATE TABLE IF NOT EXISTS notifications (
			name TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			target TEXT NOT NULL,
			metadata TEXT,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`
	tokensTable := `CREATE TABLE IF NOT EXISTS api_tokens (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			hash TEXT NOT NULL,
			scopes TEXT,
			created_at TIMESTAMP NOT NULL,
			expires_at TIMESTAMP,
			last_used_at TIMESTAMP
		);`
	policiesTable := `CREATE TABLE IF NOT EXISTS policies (
		name TEXT PRIMARY KEY,
		document TEXT NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);`
	policyVersionsTable := `CREATE TABLE IF NOT EXISTS policy_versions (
		name TEXT NOT NULL,
		version INTEGER NOT NULL,
		document TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		PRIMARY KEY (name, version)
	);`
	playbooksTable := `CREATE TABLE IF NOT EXISTS playbooks (
			name TEXT PRIMARY KEY,
			description TEXT,
			spec TEXT NOT NULL,
			tags TEXT,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`
	backupsTable := `CREATE TABLE IF NOT EXISTS backups (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			location TEXT NOT NULL,
			notes TEXT,
			created_at TIMESTAMP NOT NULL
		);`
	if driver == "postgres" {
		jobTable = `CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			status TEXT NOT NULL,
			stage TEXT,
			progress INTEGER DEFAULT 0,
			message TEXT,
			payload TEXT,
			result TEXT,
			error TEXT,
			attempt INTEGER DEFAULT 0,
			max_attempts INTEGER DEFAULT 1,
			cancelled_at TIMESTAMPTZ,
			logs TEXT,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		);`
		historyTable = `CREATE TABLE IF NOT EXISTS history (
			id BIGSERIAL PRIMARY KEY,
			event TEXT NOT NULL,
			model_id TEXT,
			metadata TEXT,
			created_at TIMESTAMPTZ NOT NULL
		);`
		hfModelsTable = `CREATE TABLE IF NOT EXISTS hf_models (
			model_id TEXT PRIMARY KEY,
			payload TEXT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		);`
		notificationsTable = `CREATE TABLE IF NOT EXISTS notifications (
			name TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			target TEXT NOT NULL,
			metadata TEXT,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		);`
		tokensTable = `CREATE TABLE IF NOT EXISTS api_tokens (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			hash TEXT NOT NULL,
			scopes TEXT,
			created_at TIMESTAMPTZ NOT NULL,
			expires_at TIMESTAMPTZ,
			last_used_at TIMESTAMPTZ
		);`
		policiesTable = `CREATE TABLE IF NOT EXISTS policies (
		name TEXT PRIMARY KEY,
		document TEXT NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	);`
		policyVersionsTable = `CREATE TABLE IF NOT EXISTS policy_versions (
		name TEXT NOT NULL,
		version INTEGER NOT NULL,
		document TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL,
		PRIMARY KEY (name, version)
	);`
		playbooksTable = `CREATE TABLE IF NOT EXISTS playbooks (
			name TEXT PRIMARY KEY,
			description TEXT,
			spec TEXT NOT NULL,
			tags TEXT,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		);`
		backupsTable = `CREATE TABLE IF NOT EXISTS backups (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			location TEXT NOT NULL,
			notes TEXT,
			created_at TIMESTAMPTZ NOT NULL
		);`
	}
	stmts = append(stmts,
		jobTable,
		`CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_type ON jobs(type);`,
		historyTable,
		hfModelsTable,
		notificationsTable,
		tokensTable,
		policiesTable,
		policyVersionsTable,
		playbooksTable,
		backupsTable,
		`CREATE TABLE IF NOT EXISTS catalog_cache (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			snapshot TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`,
	)
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("schema apply failed: %w", err)
		}
	}
	var alterStatements []string
	if driver == "postgres" {
		alterStatements = []string{
			`ALTER TABLE jobs ADD COLUMN IF NOT EXISTS attempt INTEGER DEFAULT 0`,
			`ALTER TABLE jobs ADD COLUMN IF NOT EXISTS max_attempts INTEGER DEFAULT 1`,
			`ALTER TABLE jobs ADD COLUMN IF NOT EXISTS cancelled_at TIMESTAMPTZ`,
			`ALTER TABLE jobs ADD COLUMN IF NOT EXISTS logs TEXT`,
			`ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ`,
			`ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS last_used_at TIMESTAMPTZ`,
		}
	} else {
		alterStatements = []string{
			`ALTER TABLE jobs ADD COLUMN attempt INTEGER DEFAULT 0`,
			`ALTER TABLE jobs ADD COLUMN max_attempts INTEGER DEFAULT 1`,
			`ALTER TABLE jobs ADD COLUMN cancelled_at TIMESTAMP`,
			`ALTER TABLE jobs ADD COLUMN logs TEXT`,
			`ALTER TABLE api_tokens ADD COLUMN expires_at TIMESTAMP`,
			`ALTER TABLE api_tokens ADD COLUMN last_used_at TIMESTAMP`,
		}
	}
	for _, stmt := range alterStatements {
		if err := execIgnoreDuplicate(db, stmt); err != nil {
			return err
		}
	}
	return nil
}

func execIgnoreDuplicate(db *sql.DB, stmt string) error {
	if _, err := db.Exec(stmt); err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "already exists") {
			return nil
		}
		return err
	}
	return nil
}

func (s *Store) rebind(query string) string {
	if s == nil || s.driver != "postgres" {
		return query
	}
	var builder strings.Builder
	builder.Grow(len(query) + 8)
	arg := 1
	for _, ch := range query {
		if ch == '?' {
			builder.WriteString(fmt.Sprintf("$%d", arg))
			arg++
			continue
		}
		builder.WriteRune(ch)
	}
	return builder.String()
}

// Close shuts down the datastore.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// CreateJob inserts a new job record.
func (s *Store) CreateJob(job *Job) error {
	if job.ID == "" {
		return errors.New("job id required")
	}
	now := time.Now().UTC()
	job.CreatedAt = now
	job.UpdatedAt = now
	if job.Status == "" {
		job.Status = JobPending
	}
	if job.MaxAttempts <= 0 {
		job.MaxAttempts = 1
	}
	payload, err := json.Marshal(job.Payload)
	if err != nil {
		return err
	}
	result, err := json.Marshal(job.Result)
	if err != nil {
		return err
	}
	logs, err := json.Marshal(job.Logs)
	if err != nil {
		return err
	}
	var cancelled interface{}
	if job.CancelledAt != nil && !job.CancelledAt.IsZero() {
		cancelled = *job.CancelledAt
	}
	_, err = s.db.Exec(s.rebind(`INSERT INTO jobs (id, type, status, stage, progress, message, payload, result, error, attempt, max_attempts, cancelled_at, logs, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		job.ID, job.Type, job.Status, job.Stage, job.Progress, job.Message, string(payload), string(result), job.Error, job.Attempt, job.MaxAttempts, cancelled, string(logs), job.CreatedAt, job.UpdatedAt,
	)
	return err
}

// UpdateJob mutates an existing job.
func (s *Store) UpdateJob(job *Job) error {
	job.UpdatedAt = time.Now().UTC()
	payload, err := json.Marshal(job.Payload)
	if err != nil {
		return err
	}
	result, err := json.Marshal(job.Result)
	if err != nil {
		return err
	}
	var cancelled interface{}
	if job.CancelledAt != nil && !job.CancelledAt.IsZero() {
		cancelled = *job.CancelledAt
	}
	updateLogs := len(job.Logs) > 0
	if !updateLogs {
		if existing, err := s.loadJobLogs(job.ID); err == nil && len(existing) > 0 {
			job.Logs = existing
			updateLogs = true
		}
	}
	var logsJSON string
	if updateLogs {
		data, err := json.Marshal(job.Logs)
		if err != nil {
			return err
		}
		logsJSON = string(data)
	}
	query := `UPDATE jobs SET type=?, status=?, stage=?, progress=?, message=?, payload=?, result=?, error=?, attempt=?, max_attempts=?, cancelled_at=?`
	args := []interface{}{
		job.Type, job.Status, job.Stage, job.Progress, job.Message,
		string(payload), string(result), job.Error, job.Attempt, job.MaxAttempts, cancelled,
	}
	if updateLogs {
		query += `, logs=?`
		args = append(args, logsJSON)
	}
	query += `, updated_at=? WHERE id=?`
	args = append(args, job.UpdatedAt, job.ID)

	_, err = s.db.Exec(s.rebind(query), args...)
	return err
}

// GetJob loads a job by ID.
func (s *Store) GetJob(id string) (*Job, error) {
	row := s.db.QueryRow(s.rebind(`SELECT id, type, status, stage, progress, message, payload, result, error, attempt, max_attempts, cancelled_at, logs, created_at, updated_at FROM jobs WHERE id=?`), id)
	var (
		job       Job
		payload   sql.NullString
		result    sql.NullString
		logs      sql.NullString
		cancelled sql.NullTime
	)
	if err := row.Scan(&job.ID, &job.Type, &job.Status, &job.Stage, &job.Progress, &job.Message, &payload, &result, &job.Error, &job.Attempt, &job.MaxAttempts, &cancelled, &logs, &job.CreatedAt, &job.UpdatedAt); err != nil {
		return nil, err
	}
	if payload.Valid {
		_ = json.Unmarshal([]byte(payload.String), &job.Payload)
	}
	if result.Valid {
		_ = json.Unmarshal([]byte(result.String), &job.Result)
	}
	if logs.Valid {
		_ = json.Unmarshal([]byte(logs.String), &job.Logs)
	}
	if cancelled.Valid {
		t := cancelled.Time
		job.CancelledAt = &t
	}
	return &job, nil
}

// ListJobs returns recent jobs sorted from newest to oldest.
func (s *Store) ListJobs(limit int) ([]Job, error) {
	query := `SELECT id, type, status, stage, progress, message, payload, result, error, attempt, max_attempts, cancelled_at, logs, created_at, updated_at FROM jobs ORDER BY created_at DESC`
	if limit > 0 {
		query = fmt.Sprintf("%s LIMIT %d", query, limit)
	}
	rows, err := s.db.Query(s.rebind(query))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		var j Job
		var payload, result, logs sql.NullString
		var cancelled sql.NullTime
		if err := rows.Scan(&j.ID, &j.Type, &j.Status, &j.Stage, &j.Progress, &j.Message, &payload, &result, &j.Error, &j.Attempt, &j.MaxAttempts, &cancelled, &logs, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		if payload.Valid {
			_ = json.Unmarshal([]byte(payload.String), &j.Payload)
		}
		if result.Valid {
			_ = json.Unmarshal([]byte(result.String), &j.Result)
		}
		if logs.Valid {
			_ = json.Unmarshal([]byte(logs.String), &j.Logs)
		}
		if cancelled.Valid {
			t := cancelled.Time
			j.CancelledAt = &t
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// AppendJobLog appends a log entry to the job's log list.
func (s *Store) AppendJobLog(jobID string, entry JobLogEntry) error {
	if s == nil || s.db == nil {
		return errors.New("store not initialized")
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	job, err := s.GetJob(jobID)
	if err != nil {
		return err
	}
	job.Logs = append(job.Logs, entry)
	return s.UpdateJob(job)
}

// CountJobsByStatus returns counts keyed by job status.
func (s *Store) CountJobsByStatus() (map[JobStatus]int, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[JobStatus]int)
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		result[JobStatus(status)] = count
	}
	return result, rows.Err()
}

func (s *Store) loadJobLogs(id string) ([]JobLogEntry, error) {
	if id == "" {
		return nil, nil
	}
	row := s.db.QueryRow(s.rebind(`SELECT logs FROM jobs WHERE id=?`), id)
	var logs sql.NullString
	if err := row.Scan(&logs); err != nil {
		return nil, err
	}
	if !logs.Valid || logs.String == "" {
		return nil, nil
	}
	var entries []JobLogEntry
	if err := json.Unmarshal([]byte(logs.String), &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// AppendHistory writes an entry to the history log.
func (s *Store) AppendHistory(entry *HistoryEntry) error {
	entry.CreatedAt = time.Now().UTC()
	metadata, err := json.Marshal(entry.Metadata)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(s.rebind(`INSERT INTO history (event, model_id, metadata, created_at) VALUES (?, ?, ?, ?)`),
		entry.Event, entry.ModelID, string(metadata), entry.CreatedAt,
	)
	if err != nil {
		return err
	}
	if id, err := res.LastInsertId(); err == nil {
		entry.ID = fmt.Sprintf("%d", id)
	}
	return nil
}

// ReplaceHFModels replaces cached Hugging Face models.
func (s *Store) ReplaceHFModels(models []vllm.HuggingFaceModel) error {
	if s == nil || s.db == nil {
		return errors.New("store not initialized")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`DELETE FROM hf_models`); err != nil {
		return err
	}
	stmt, err := tx.Prepare(s.rebind(`INSERT INTO hf_models (model_id, payload, updated_at) VALUES (?, ?, ?)`))
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := time.Now().UTC()
	for _, model := range models {
		id := canonicalModelID(model)
		if id == "" {
			continue
		}
		payload, err := json.Marshal(model)
		if err != nil {
			return err
		}
		if _, err := stmt.Exec(id, string(payload), now); err != nil {
			return err
		}
	}
	err = tx.Commit()
	return err
}

// ListHFModels returns cached Hugging Face models.
func (s *Store) ListHFModels() ([]vllm.HuggingFaceModel, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store not initialized")
	}
	rows, err := s.db.Query(`SELECT payload FROM hf_models ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var models []vllm.HuggingFaceModel
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var model vllm.HuggingFaceModel
		if err := json.Unmarshal([]byte(payload), &model); err != nil {
			continue
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

// GetHFModel fetches a single cached HF model.
func (s *Store) GetHFModel(id string) (*vllm.HuggingFaceModel, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store not initialized")
	}
	id = strings.TrimSpace(strings.ToLower(id))
	if id == "" {
		return nil, errors.New("model id required")
	}
	query := s.rebind(`SELECT payload FROM hf_models WHERE model_id=?`)
	var payload string
	err := s.db.QueryRow(query, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var model vllm.HuggingFaceModel
	if err := json.Unmarshal([]byte(payload), &model); err != nil {
		return nil, err
	}
	return &model, nil
}

func canonicalModelID(model vllm.HuggingFaceModel) string {
	if strings.TrimSpace(model.ModelID) != "" {
		return strings.ToLower(model.ModelID)
	}
	return strings.ToLower(model.ID)
}

func encodeStringSlice(values []string) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func decodeStringSlice(payload string) []string {
	payload = strings.TrimSpace(payload)
	if payload == "" {
		return nil
	}
	var values []string
	if err := json.Unmarshal([]byte(payload), &values); err != nil {
		return nil
	}
	return values
}

// ListHistory returns the newest history entries.
func (s *Store) ListHistory(limit int) ([]HistoryEntry, error) {
	query := `SELECT id, event, model_id, metadata, created_at FROM history ORDER BY id DESC`
	if limit > 0 {
		query = fmt.Sprintf("%s LIMIT %d", query, limit)
	}
	rows, err := s.db.Query(s.rebind(query))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		var metadata sql.NullString
		var id int64
		if err := rows.Scan(&id, &e.Event, &e.ModelID, &metadata, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.ID = fmt.Sprintf("%d", id)
		if metadata.Valid {
			_ = json.Unmarshal([]byte(metadata.String), &e.Metadata)
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// DeleteJobs removes jobs optionally filtered by status.
func (s *Store) DeleteJobs(status string) error {
	if s == nil || s.db == nil {
		return errors.New("datastore not configured")
	}
	query := "DELETE FROM jobs"
	var args []interface{}
	if status != "" {
		query += " WHERE status = ?"
		args = append(args, status)
	}
	_, err := s.db.Exec(s.rebind(query), args...)
	return err
}

// CleanupJobsBefore removes completed jobs older than the provided timestamp.
func (s *Store) CleanupJobsBefore(ts time.Time, statuses ...JobStatus) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("datastore not configured")
	}
	query := "DELETE FROM jobs WHERE updated_at < ?"
	args := []interface{}{ts}
	if len(statuses) > 0 {
		placeholders := make([]string, 0, len(statuses))
		for _, st := range statuses {
			placeholders = append(placeholders, "?")
			args = append(args, st)
		}
		query += " AND status IN (" + strings.Join(placeholders, ",") + ")"
	}
	res, err := s.db.Exec(s.rebind(query), args...)
	if err != nil {
		return 0, err
	}
	rows, _ := res.RowsAffected()
	return rows, nil
}

// ClearHistory deletes all history entries.
func (s *Store) ClearHistory() error {
	if s == nil || s.db == nil {
		return errors.New("datastore not configured")
	}
	_, err := s.db.Exec("DELETE FROM history")
	return err
}

// CleanupHistoryBefore deletes entries older than the provided timestamp.
func (s *Store) CleanupHistoryBefore(ts time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("datastore not configured")
	}
	res, err := s.db.Exec(s.rebind(`DELETE FROM history WHERE created_at < ?`), ts)
	if err != nil {
		return 0, err
	}
	rows, _ := res.RowsAffected()
	return rows, nil
}

// SaveCatalogSnapshot persists the catalog contents for reuse when git-sync is cold.
func (s *Store) SaveCatalogSnapshot(models []*catalog.Model) error {
	if s == nil || s.db == nil {
		return errors.New("datastore not configured")
	}
	data, err := json.Marshal(models)
	if err != nil {
		return fmt.Errorf("failed to marshal catalog snapshot: %w", err)
	}
	_, err = s.db.Exec(s.rebind(`INSERT INTO catalog_cache (id, snapshot, updated_at)
		VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET snapshot=excluded.snapshot, updated_at=excluded.updated_at`),
		string(data), time.Now().UTC(),
	)
	return err
}

// LoadCatalogSnapshot pulls the last catalog snapshot.
func (s *Store) LoadCatalogSnapshot() ([]*catalog.Model, time.Time, error) {
	if s == nil || s.db == nil {
		return nil, time.Time{}, errors.New("datastore not configured")
	}
	row := s.db.QueryRow(s.rebind(`SELECT snapshot, updated_at FROM catalog_cache WHERE id = 1`))
	var snapshot string
	var updated time.Time
	if err := row.Scan(&snapshot, &updated); err != nil {
		return nil, time.Time{}, err
	}
	var models []*catalog.Model
	if err := json.Unmarshal([]byte(snapshot), &models); err != nil {
		return nil, time.Time{}, fmt.Errorf("failed to decode catalog snapshot: %w", err)
	}
	return models, updated, nil
}

// UpsertNotification creates or updates a notification channel.
func (s *Store) UpsertNotification(n *Notification) error {
	if s == nil || s.db == nil {
		return errors.New("datastore not configured")
	}
	if n == nil || n.Name == "" {
		return errors.New("invalid notification")
	}
	now := time.Now().UTC()
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	n.UpdatedAt = now
	metaJSON := ""
	if len(n.Metadata) > 0 {
		if buf, err := json.Marshal(n.Metadata); err == nil {
			metaJSON = string(buf)
		}
	}
	query := s.rebind(`INSERT INTO notifications (name, type, target, metadata, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			type=excluded.type,
			target=excluded.target,
			metadata=excluded.metadata,
			updated_at=excluded.updated_at`)
	_, err := s.db.Exec(query, n.Name, n.Type, n.Target, metaJSON, n.CreatedAt, n.UpdatedAt)
	return err
}

// ListNotifications returns configured channels.
func (s *Store) ListNotifications() ([]Notification, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("datastore not configured")
	}
	rows, err := s.db.Query(`SELECT name, type, target, metadata, created_at, updated_at FROM notifications ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []Notification
	for rows.Next() {
		var rec Notification
		var metadata sql.NullString
		if err := rows.Scan(&rec.Name, &rec.Type, &rec.Target, &metadata, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		if metadata.Valid && metadata.String != "" {
			var meta map[string]string
			if err := json.Unmarshal([]byte(metadata.String), &meta); err == nil {
				rec.Metadata = meta
			}
		}
		channels = append(channels, rec)
	}
	return channels, nil
}

// GetNotification returns a single channel by name.
func (s *Store) GetNotification(name string) (*Notification, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("datastore not configured")
	}
	var rec Notification
	var metadata sql.NullString
	row := s.db.QueryRow(s.rebind(`SELECT name, type, target, metadata, created_at, updated_at FROM notifications WHERE name = ?`), name)
	if err := row.Scan(&rec.Name, &rec.Type, &rec.Target, &metadata, &rec.CreatedAt, &rec.UpdatedAt); err != nil {
		return nil, err
	}
	if metadata.Valid && metadata.String != "" {
		_ = json.Unmarshal([]byte(metadata.String), &rec.Metadata)
	}
	return &rec, nil
}

// NotificationHealth aggregates delivery stats across history.
func (s *Store) NotificationHealth() (NotificationStats, error) {
	stats := NotificationStats{}
	if s == nil || s.db == nil {
		return stats, errors.New("datastore not configured")
	}
	row := s.db.QueryRow(`SELECT COUNT(*) FROM notifications`)
	_ = row.Scan(&stats.Channels)
	rows, err := s.db.Query(`SELECT event, COUNT(*), MAX(created_at) FROM history WHERE event IN ('notification_test','notification_delivery','notification_failed') GROUP BY event`)
	if err != nil {
		return stats, err
	}
	defer rows.Close()
	var latest time.Time
	for rows.Next() {
		var event string
		var count int
		var last time.Time
		if err := rows.Scan(&event, &count, &last); err != nil {
			return stats, err
		}
		switch event {
		case "notification_test":
			stats.Tested = count
		case "notification_delivery":
			stats.Delivered = count
		case "notification_failed":
			stats.Failed = count
		}
		if last.After(latest) {
			copy := last
			latest = last
			stats.LastEvent = &copy
		}
	}
	return stats, rows.Err()
}

// DeleteNotification removes a notification channel.
func (s *Store) DeleteNotification(name string) error {
	if s == nil || s.db == nil {
		return errors.New("datastore not configured")
	}
	result, err := s.db.Exec(s.rebind(`DELETE FROM notifications WHERE name = ?`), name)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CreateAPIToken stores metadata for an API token (hash only).
func (s *Store) CreateAPIToken(t *APIToken) error {
	if s == nil || s.db == nil {
		return errors.New("datastore not configured")
	}
	if t == nil || t.ID == "" || t.Hash == "" {
		return errors.New("invalid token payload")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = time.Now().UTC()
	}
	scopeStr := strings.Join(t.Scopes, ",")
	_, err := s.db.Exec(s.rebind(`INSERT INTO api_tokens (id, name, hash, scopes, created_at, expires_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?, ?)`),
		t.ID, t.Name, t.Hash, scopeStr, t.CreatedAt, t.ExpiresAt, t.LastUsedAt)
	return err
}

// ListAPITokens enumerates issued tokens (hash omitted).
func (s *Store) ListAPITokens() ([]APIToken, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("datastore not configured")
	}
	rows, err := s.db.Query(`SELECT id, name, scopes, created_at, expires_at, last_used_at FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []APIToken
	for rows.Next() {
		var rec APIToken
		var scopes sql.NullString
		var expires, lastUsed sql.NullTime
		if err := rows.Scan(&rec.ID, &rec.Name, &scopes, &rec.CreatedAt, &expires, &lastUsed); err != nil {
			return nil, err
		}
		if scopes.Valid && scopes.String != "" {
			rec.Scopes = strings.Split(scopes.String, ",")
		}
		if expires.Valid {
			t := expires.Time
			rec.ExpiresAt = &t
		}
		if lastUsed.Valid {
			t := lastUsed.Time
			rec.LastUsedAt = &t
		}
		tokens = append(tokens, rec)
	}
	return tokens, nil
}

// DeleteAPIToken removes a token by ID.
func (s *Store) DeleteAPIToken(id string) error {
	if s == nil || s.db == nil {
		return errors.New("datastore not configured")
	}
	result, err := s.db.Exec(s.rebind(`DELETE FROM api_tokens WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ValidateAPITokenHash checks whether a hash exists.
func (s *Store) ValidateAPITokenHash(hash string) (bool, error) {
	if _, err := s.LookupAPITokenByHash(hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// LookupAPITokenByHash returns a token record for the provided hash.
func (s *Store) LookupAPITokenByHash(hash string) (*APIToken, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("datastore not configured")
	}
	var rec APIToken
	var scopes sql.NullString
	var expires, lastUsed sql.NullTime
	row := s.db.QueryRow(s.rebind(`SELECT id, name, scopes, created_at, expires_at, last_used_at FROM api_tokens WHERE hash = ? LIMIT 1`), hash)
	if err := row.Scan(&rec.ID, &rec.Name, &scopes, &rec.CreatedAt, &expires, &lastUsed); err != nil {
		return nil, err
	}
	if scopes.Valid && scopes.String != "" {
		rec.Scopes = strings.Split(scopes.String, ",")
	}
	if expires.Valid {
		t := expires.Time
		rec.ExpiresAt = &t
	}
	if lastUsed.Valid {
		t := lastUsed.Time
		rec.LastUsedAt = &t
	}
	return &rec, nil
}

// TouchAPIToken updates the last-used timestamp for a token.
func (s *Store) TouchAPIToken(id string) error {
	if s == nil || s.db == nil {
		return errors.New("datastore not configured")
	}
	_, err := s.db.Exec(s.rebind(`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`), time.Now().UTC(), id)
	return err
}

// UpsertPolicy stores or updates a policy document.
func (s *Store) UpsertPolicy(p *Policy) error {
	if s == nil || s.db == nil {
		return errors.New("datastore not configured")
	}
	if p == nil || p.Name == "" {
		return errors.New("invalid policy")
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	if current, err := s.GetPolicy(p.Name); err == nil && current != nil && current.Document != "" {
		_ = s.snapshotPolicyVersion(current)
	}
	query := s.rebind(`INSERT INTO policies (name, document, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET document=excluded.document, updated_at=excluded.updated_at`)
	_, err := s.db.Exec(query, p.Name, p.Document, p.UpdatedAt)
	return err
}

// GetPolicy returns a stored policy by name.
func (s *Store) GetPolicy(name string) (*Policy, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("datastore not configured")
	}
	row := s.db.QueryRow(s.rebind(`SELECT name, document, updated_at FROM policies WHERE name = ?`), name)
	var policy Policy
	if err := row.Scan(&policy.Name, &policy.Document, &policy.UpdatedAt); err != nil {
		return nil, err
	}
	return &policy, nil
}

func (s *Store) snapshotPolicyVersion(p *Policy) error {
	if p == nil || p.Name == "" {
		return nil
	}
	var version int
	_ = s.db.QueryRow(s.rebind(`SELECT COALESCE(MAX(version), 0) FROM policy_versions WHERE name = ?`), p.Name).Scan(&version)
	version++
	created := p.UpdatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, err := s.db.Exec(s.rebind(`INSERT INTO policy_versions (name, version, document, created_at) VALUES (?, ?, ?, ?)`),
		p.Name, version, p.Document, created)
	return err
}

// ListPolicyVersions returns previous revisions for a policy.
func (s *Store) ListPolicyVersions(name string, limit int) ([]PolicyVersion, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("datastore not configured")
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(s.rebind(`SELECT version, document, created_at FROM policy_versions WHERE name = ? ORDER BY version DESC LIMIT ?`), name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var versions []PolicyVersion
	for rows.Next() {
		var v PolicyVersion
		v.Name = name
		if err := rows.Scan(&v.Version, &v.Document, &v.CreatedAt); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

// RollbackPolicy restores a prior revision.
func (s *Store) RollbackPolicy(name string, version int) (*Policy, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("datastore not configured")
	}
	query := `SELECT version, document, created_at FROM policy_versions WHERE name = ?`
	args := []interface{}{name}
	if version > 0 {
		query += " AND version = ?"
		args = append(args, version)
	}
	query += " ORDER BY version DESC LIMIT 1"
	row := s.db.QueryRow(s.rebind(query), args...)
	var selected PolicyVersion
	if err := row.Scan(&selected.Version, &selected.Document, &selected.CreatedAt); err != nil {
		return nil, err
	}
	policy := &Policy{Name: name, Document: selected.Document, UpdatedAt: time.Now().UTC()}
	if err := s.UpsertPolicy(policy); err != nil {
		return nil, err
	}
	return policy, nil
}

// ListPolicies returns stored policies.
func (s *Store) ListPolicies() ([]Policy, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("datastore not configured")
	}
	rows, err := s.db.Query(`SELECT name, document, updated_at FROM policies ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var policies []Policy
	for rows.Next() {
		var rec Policy
		if err := rows.Scan(&rec.Name, &rec.Document, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		policies = append(policies, rec)
	}
	return policies, nil
}

// DeletePolicy removes a policy by name.
func (s *Store) DeletePolicy(name string) error {
	if s == nil || s.db == nil {
		return errors.New("datastore not configured")
	}
	result, err := s.db.Exec(s.rebind(`DELETE FROM policies WHERE name = ?`), name)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GenerateToken creates a random token string and its SHA-256 hash.
func GenerateToken(length int) (string, string, error) {
	if length <= 0 {
		length = 32
	}
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	plain := base64.RawURLEncoding.EncodeToString(buf)
	return plain, HashToken(plain), nil
}

// HashToken returns the persistent hash for a plaintext token.
func HashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// RecordBackup stores metadata for a backup run.
func (s *Store) RecordBackup(b *Backup) error {
	if s == nil || s.db == nil {
		return errors.New("datastore not configured")
	}
	if b == nil || b.ID == "" {
		return errors.New("invalid backup record")
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = time.Now().UTC()
	}
	query := s.rebind(`INSERT INTO backups (id, type, location, notes, created_at) VALUES (?, ?, ?, ?, ?)`)
	_, err := s.db.Exec(query, b.ID, b.Type, b.Location, b.Notes, b.CreatedAt)
	return err
}

// ListBackups returns recorded backups ordered by recency.
func (s *Store) ListBackups(limit int) ([]Backup, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("datastore not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(s.rebind(`SELECT id, type, location, notes, created_at FROM backups ORDER BY created_at DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []Backup
	for rows.Next() {
		var rec Backup
		if err := rows.Scan(&rec.ID, &rec.Type, &rec.Location, &rec.Notes, &rec.CreatedAt); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

// ListPlaybooks returns all stored playbooks sorted by name.
func (s *Store) ListPlaybooks() ([]Playbook, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("datastore not configured")
	}
	rows, err := s.db.Query(`SELECT name, description, spec, tags, created_at, updated_at FROM playbooks ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Playbook
	for rows.Next() {
		var (
			pb        Playbook
			spec      string
			tagString sql.NullString
		)
		if err := rows.Scan(&pb.Name, &pb.Description, &spec, &tagString, &pb.CreatedAt, &pb.UpdatedAt); err != nil {
			return nil, err
		}
		pb.Spec = json.RawMessage(spec)
		if tagString.Valid && tagString.String != "" {
			pb.Tags = decodeStringSlice(tagString.String)
		}
		items = append(items, pb)
	}
	return items, rows.Err()
}

// GetPlaybook fetches a playbook by name.
func (s *Store) GetPlaybook(name string) (*Playbook, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("datastore not configured")
	}
	row := s.db.QueryRow(s.rebind(`SELECT name, description, spec, tags, created_at, updated_at FROM playbooks WHERE name=?`), name)
	var (
		pb        Playbook
		spec      string
		tagString sql.NullString
	)
	if err := row.Scan(&pb.Name, &pb.Description, &spec, &tagString, &pb.CreatedAt, &pb.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPlaybookNotFound
		}
		return nil, err
	}
	pb.Spec = json.RawMessage(spec)
	if tagString.Valid && tagString.String != "" {
		pb.Tags = decodeStringSlice(tagString.String)
	}
	return &pb, nil
}

// UpsertPlaybook creates or updates a playbook definition.
func (s *Store) UpsertPlaybook(pb *Playbook) (*Playbook, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("datastore not configured")
	}
	if pb == nil {
		return nil, errors.New("playbook is required")
	}
	now := time.Now().UTC()
	if pb.CreatedAt.IsZero() {
		pb.CreatedAt = now
	}
	pb.UpdatedAt = now
	tagPayload, err := encodeStringSlice(pb.Tags)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(s.rebind(`INSERT INTO playbooks (name, description, spec, tags, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET description=excluded.description, spec=excluded.spec, tags=excluded.tags, updated_at=excluded.updated_at`),
		pb.Name, pb.Description, string(pb.Spec), tagPayload, pb.CreatedAt, pb.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return pb, nil
}

// DeletePlaybook removes a stored playbook.
func (s *Store) DeletePlaybook(name string) error {
	if s == nil || s.db == nil {
		return errors.New("datastore not configured")
	}
	result, err := s.db.Exec(s.rebind(`DELETE FROM playbooks WHERE name=?`), name)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return ErrPlaybookNotFound
	}
	return nil
}
