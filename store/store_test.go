package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendAndPurge(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now.AddDate(-2, 0, 0) }
	if err := s.AddFeedback(Feedback{Day: s.Today(), Comment: "старый"}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now }
	s.AddFeedback(Feedback{Day: s.Today(), Comment: "новый"})
	s.AddSituation(Situation{Day: s.Today(), Via: "quiz", Facts: map[string]string{"transport": "hired_carrier"}})
	// испорченная строка остаётся для человека
	f, _ := os.OpenFile(filepath.Join(dir, feedbackFile), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("{испорчено\n")
	f.Close()

	if err := s.Purge(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, feedbackFile))
	got := string(data)
	if strings.Contains(got, "старый") || !strings.Contains(got, "новый") || !strings.Contains(got, "{испорчено") {
		t.Errorf("после чистки:\n%s", got)
	}
	if st, _ := os.Stat(filepath.Join(dir, feedbackFile)); st.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Errorf("права %v", st.Mode().Perm())
	}
	sit, _ := os.ReadFile(filepath.Join(dir, situationsFile))
	if !strings.Contains(string(sit), `"day":"2026-09-24"`) {
		t.Errorf("журнал ситуаций: %s", sit)
	}
	for _, bad := range []string{"ip", "cookie", "user_agent", "T12:"} {
		if strings.Contains(strings.ToLower(got+string(sit)), bad) {
			t.Errorf("в журнале лишнее: %q", bad)
		}
	}
}
