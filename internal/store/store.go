package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// JobStatus represents asynchronous job state.
type JobStatus string

const (
	JobPending JobStatus = "pending"
	JobRunning JobStatus = "running"
	JobDone    JobStatus = "completed"
	JobFailed  JobStatus = "failed"
)

// Job represents an asynchronous task (e.g., weight installation).
type Job struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Status    JobStatus              `json:"status"`
	Stage     string                 `json:"stage,omitempty"`
	Progress  int                    `json:"progress,omitempty"`
	Message   string                 `json:"message,omitempty"`
	Payload   map[string]interface{} `json:"payload,omitempty"`
	Result    map[string]interface{} `json:"result,omitempty"`
	Error     string                 `json:"error,omitempty"`
	CreatedAt time.Time              `json:"createdAt"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

// HistoryEntry stores past actions (installations, activations, etc.).
type HistoryEntry struct {
	ID        string                 `json:"id"`
	Event     string                 `json:"event"`
	ModelID   string                 `json:"modelId,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt time.Time              `json:"createdAt"`
}

// Store wraps the SQLite database used for persistence.
type Store struct {
	db *sql.DB
}

// Open initializes the datastore using the supplied DSN/file path and driver.
func Open(dsn string, driver string) (*Store, error) {
	if driver == "" {
		driver = "sqlite"
	}
	if driver != "sqlite" {
		return nil, fmt.Errorf("unsupported datastore driver: %s", driver)
	}
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("datastore DSN is required")
	}
	if err := os.MkdirAll(filepath.Dir(dsn), 0o755); err != nil {
		return nil, fmt.Errorf("failed to create datastore directory: %w", err)
	}
	conn := fmt.Sprintf("file:%s?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on", dsn)
	db, err := sql.Open("sqlite", conn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite datastore: %w", err)
	}
	if err := initSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func initSchema(db *sql.DB) error {
	stmts := []string{
		`PRAGMA journal_mode=WAL;`,
		`CREATE TABLE IF NOT EXISTS jobs (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			status TEXT NOT NULL,
			stage TEXT,
			progress INTEGER DEFAULT 0,
			message TEXT,
			payload TEXT,
			result TEXT,
			error TEXT,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_type ON jobs(type);`,
		`CREATE TABLE IF NOT EXISTS history (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			event TEXT NOT NULL,
			model_id TEXT,
			metadata TEXT,
			created_at TIMESTAMP NOT NULL
		);`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("schema apply failed: %w", err)
		}
	}
	return nil
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
	payload, err := json.Marshal(job.Payload)
	if err != nil {
		return err
	}
	result, err := json.Marshal(job.Result)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO jobs (id, type, status, stage, progress, message, payload, result, error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Type, job.Status, job.Stage, job.Progress, job.Message, string(payload), string(result), job.Error, job.CreatedAt, job.UpdatedAt,
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
	_, err = s.db.Exec(`UPDATE jobs SET type=?, status=?, stage=?, progress=?, message=?, payload=?, result=?, error=?, updated_at=? WHERE id=?`,
		job.Type, job.Status, job.Stage, job.Progress, job.Message, string(payload), string(result), job.Error, job.UpdatedAt, job.ID,
	)
	return err
}

// GetJob loads a job by ID.
func (s *Store) GetJob(id string) (*Job, error) {
	row := s.db.QueryRow(`SELECT id, type, status, stage, progress, message, payload, result, error, created_at, updated_at FROM jobs WHERE id=?`, id)
	var (
		job     Job
		payload sql.NullString
		result  sql.NullString
	)
	if err := row.Scan(&job.ID, &job.Type, &job.Status, &job.Stage, &job.Progress, &job.Message, &payload, &result, &job.Error, &job.CreatedAt, &job.UpdatedAt); err != nil {
		return nil, err
	}
	if payload.Valid {
		_ = json.Unmarshal([]byte(payload.String), &job.Payload)
	}
	if result.Valid {
		_ = json.Unmarshal([]byte(result.String), &job.Result)
	}
	return &job, nil
}

// ListJobs returns recent jobs sorted from newest to oldest.
func (s *Store) ListJobs(limit int) ([]Job, error) {
	query := `SELECT id, type, status, stage, progress, message, payload, result, error, created_at, updated_at FROM jobs ORDER BY created_at DESC`
	if limit > 0 {
		query = fmt.Sprintf("%s LIMIT %d", query, limit)
	}
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		var j Job
		var payload, result sql.NullString
		if err := rows.Scan(&j.ID, &j.Type, &j.Status, &j.Stage, &j.Progress, &j.Message, &payload, &result, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		if payload.Valid {
			_ = json.Unmarshal([]byte(payload.String), &j.Payload)
		}
		if result.Valid {
			_ = json.Unmarshal([]byte(result.String), &j.Result)
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// AppendHistory writes an entry to the history log.
func (s *Store) AppendHistory(entry *HistoryEntry) error {
	entry.CreatedAt = time.Now().UTC()
	metadata, err := json.Marshal(entry.Metadata)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`INSERT INTO history (event, model_id, metadata, created_at) VALUES (?, ?, ?, ?)`,
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

// ListHistory returns the newest history entries.
func (s *Store) ListHistory(limit int) ([]HistoryEntry, error) {
	query := `SELECT id, event, model_id, metadata, created_at FROM history ORDER BY id DESC`
	if limit > 0 {
		query = fmt.Sprintf("%s LIMIT %d", query, limit)
	}
	rows, err := s.db.Query(query)
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
