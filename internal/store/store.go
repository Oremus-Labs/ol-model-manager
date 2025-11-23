package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	jobBucket     = "jobs"
	historyBucket = "history"
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

// Store wraps BoltDB for job/state persistence.
type Store struct {
	db *bolt.DB
}

// Open initializes the store at the given directory.
func Open(stateDir string) (*Store, error) {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}
	path := filepath.Join(stateDir, "state.db")
	db, err := bolt.Open(path, 0o644, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("failed to open state db: %w", err)
	}

	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists([]byte(jobBucket)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(historyBucket)); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return &Store{db: db}, nil
}

// Close closes the underlying DB.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// CreateJob persists a new job record.
func (s *Store) CreateJob(job *Job) error {
	now := time.Now()
	job.CreatedAt = now
	job.UpdatedAt = now
	if job.Status == "" {
		job.Status = JobPending
	}
	return s.save(jobBucket, job.ID, job)
}

// UpdateJob updates an existing job.
func (s *Store) UpdateJob(job *Job) error {
	job.UpdatedAt = time.Now()
	return s.save(jobBucket, job.ID, job)
}

// GetJob loads a job by ID.
func (s *Store) GetJob(id string) (*Job, error) {
	var job Job
	if err := s.load(jobBucket, id, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

// ListJobs returns recent jobs (sorted newest -> oldest).
func (s *Store) ListJobs(limit int) ([]Job, error) {
	var jobs []Job
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(jobBucket))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var job Job
			if err := json.Unmarshal(v, &job); err != nil {
				continue
			}
			jobs = append(jobs, job)
			if limit > 0 && len(jobs) >= limit {
				break
			}
		}
		return nil
	})
	return jobs, err
}

// AppendHistory records a new history entry.
func (s *Store) AppendHistory(entry *HistoryEntry) error {
	if entry.ID == "" {
		entry.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	entry.CreatedAt = time.Now()
	return s.save(historyBucket, entry.ID, entry)
}

// ListHistory returns newest history entries (limit optional).
func (s *Store) ListHistory(limit int) ([]HistoryEntry, error) {
	var entries []HistoryEntry
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(historyBucket))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var entry HistoryEntry
			if err := json.Unmarshal(v, &entry); err != nil {
				continue
			}
			entries = append(entries, entry)
			if limit > 0 && len(entries) >= limit {
				break
			}
		}
		return nil
	})
	return entries, err
}

func (s *Store) save(bucket string, key string, value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket %s not found", bucket)
		}
		return b.Put([]byte(key), data)
	})
}

func (s *Store) load(bucket, key string, out interface{}) error {
	return s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("bucket %s not found", bucket)
		}
		value := b.Get([]byte(key))
		if value == nil {
			return fmt.Errorf("record not found")
		}
		return json.Unmarshal(value, out)
	})
}
