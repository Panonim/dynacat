package dynglance

import (
	"database/sql"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"
)

type todoTask struct {
	Text    string `json:"text"`
	Checked bool   `json:"checked"`
}

type todoStorage struct {
	path string
	once sync.Once
	db   *sql.DB
	err  error
}

func newTodoStorage(path string) *todoStorage {
	return &todoStorage{path: path}
}

func (s *todoStorage) init() error {
	s.once.Do(func() {
		db, err := sql.Open("sqlite", s.path)
		if err != nil {
			s.err = fmt.Errorf("opening sqlite db: %w", err)
			return
		}

		_, err = db.Exec(`CREATE TABLE IF NOT EXISTS todo_tasks (
			list_id  TEXT    NOT NULL,
			position INTEGER NOT NULL,
			text     TEXT    NOT NULL,
			checked  INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (list_id, position)
		)`)
		if err != nil {
			db.Close()
			s.err = fmt.Errorf("creating todo_tasks table: %w", err)
			return
		}

		s.db = db
	})
	return s.err
}

func (s *todoStorage) loadTasks(listID string) ([]todoTask, error) {
	if err := s.init(); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(
		`SELECT text, checked FROM todo_tasks WHERE list_id = ? ORDER BY position`,
		listID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying tasks: %w", err)
	}
	defer rows.Close()

	var tasks []todoTask
	for rows.Next() {
		var t todoTask
		var checked int
		if err := rows.Scan(&t.Text, &checked); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		t.Checked = checked != 0
		tasks = append(tasks, t)
	}

	if tasks == nil {
		tasks = []todoTask{}
	}

	return tasks, rows.Err()
}

func (s *todoStorage) saveTasks(listID string, tasks []todoTask) error {
	if err := s.init(); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err = tx.Exec(`DELETE FROM todo_tasks WHERE list_id = ?`, listID); err != nil {
		return fmt.Errorf("deleting tasks: %w", err)
	}

	for i, t := range tasks {
		checked := 0
		if t.Checked {
			checked = 1
		}
		if _, err = tx.Exec(
			`INSERT INTO todo_tasks (list_id, position, text, checked) VALUES (?, ?, ?, ?)`,
			listID, i, t.Text, checked,
		); err != nil {
			return fmt.Errorf("inserting task: %w", err)
		}
	}

	return tx.Commit()
}

func (s *todoStorage) close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}
