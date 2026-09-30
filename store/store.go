// Package store — журналы этапа 4 (бриф §8): отзывы «В моей ситуации это не так» и журнал
// ситуаций. Файлы JSON Lines в каталоге данных; записи старше Keep удаляются.
//
// Что сюда НЕ пишется никогда: IP-адреса, cookie, заголовки запроса, текст описания для ИИ.
// Время — только день: точное время вместе с журналом хостинга могло бы связать запись с человеком.
package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Keep — срок хранения записей (политика обработки данных, разделы об отзывах и журнале).
const Keep = 365 * 24 * time.Hour

// Situation — итог опроса: ответы (варианты из списка) и вывод движка. Персональных данных нет.
type Situation struct {
	Day       string            `json:"day"`
	Via       string            `json:"via"`              // quiz | ai (сайт), max | telegram (боты)
	Source    string            `json:"source,omitempty"` // метка ссылки на бота (?start=…) из разрешённого списка или other
	Facts     map[string]string `json:"facts"`
	Rules     []string          `json:"rules"`
	Certainty string            `json:"certainty"`
	Found     bool              `json:"found"`
	RulesVer  string            `json:"rules_version"`
}

// Feedback — отзыв «не так». Comment уже очищен от e-mail, телефонов и номеров.
type Feedback struct {
	Day       string            `json:"day"`
	Facts     map[string]string `json:"facts"`
	Rules     []string          `json:"rules"`
	Comment   string            `json:"comment"`
	Source    string            `json:"source,omitempty"` // для отзывов из ботов — как у Situation
	Policy    string            `json:"policy"`           // редакция политики, которую видел человек, например privacy.v2
	PolicySHA string            `json:"policy_sha"`       // sha256 её текста с реквизитами
	RulesVer  string            `json:"rules_version"`
}

type Store struct {
	dir string
	mu  sync.Mutex
	now func() time.Time
}

const (
	feedbackFile   = "feedback.jsonl"
	situationsFile = "situations.jsonl"
)

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, now: time.Now}
	return s, s.Purge()
}

// Today — день записи (UTC).
func (s *Store) Today() string { return s.now().UTC().Format(time.DateOnly) }

func (s *Store) AddSituation(r Situation) error { return s.append(situationsFile, r) }
func (s *Store) AddFeedback(r Feedback) error   { return s.append(feedbackFile, r) }

func (s *Store) append(name string, v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Purge удаляет записи старше Keep из обоих журналов.
func (s *Store) Purge() error {
	cut := s.now().Add(-Keep).UTC().Format(time.DateOnly)
	for _, name := range []string{feedbackFile, situationsFile} {
		if err := s.purge(name, cut); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) purge(name, cut string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, name)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var keep bytes.Buffer
	dropped := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var rec struct {
			Day string `json:"day"`
		}
		// испорченную строку не выбрасываем молча: её разберёт человек
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Day != "" && rec.Day < cut {
			dropped++
			continue
		}
		keep.Write(sc.Bytes())
		keep.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if dropped == 0 {
		return nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, keep.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// PurgeDaily — чистка раз в сутки, пока жив процесс.
func (s *Store) PurgeDaily(logf func(string, ...any)) {
	go func() {
		for range time.Tick(24 * time.Hour) {
			if err := s.Purge(); err != nil {
				logf("store: чистка: %v", err)
			}
		}
	}()
}
