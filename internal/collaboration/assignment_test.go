package collaboration

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func assignmentFixture(t *testing.T) (Store, Assignment) {
	t.Helper()
	root := t.TempDir()
	a := Assignment{Author: Participant{Name: "author", Pane: "ws:p1", Role: "implementer", Family: "openai", Started: "one", Session: "session-one", Cwd: root}, Reviewer: Participant{Name: "reviewer", Pane: "ws:p2", Role: "reviewer", Family: "anthropic", Started: "two", Session: "session-two", Cwd: root}, Workspace: "ws", Root: root, BriefHash: "brief", Paths: []string{"file.go"}}
	s := Store{StateDir: t.TempDir()}
	created, err := s.Create(a)
	if err != nil {
		t.Fatal(err)
	}
	return s, created
}

func TestAssignmentGenerationAllowsOneConcurrentTransition(t *testing.T) {
	s, a := assignmentFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Update(a.ID, a.Generation, func(current *Assignment) error { current.Phase = Reviewing; return nil })
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	success := 0
	for err := range errs {
		if err == nil {
			success++
		}
	}
	current, err := s.Read(a.ID)
	if err != nil || success != 1 || current.Generation != 2 || current.Phase != Reviewing {
		t.Fatalf("success=%d record=%+v err=%v", success, current, err)
	}
}

func TestAssignmentIdentityAndUnreadableStateFailClosed(t *testing.T) {
	s, a := assignmentFixture(t)
	if _, err := s.Create(a); err == nil {
		t.Fatal("second active assignment accepted")
	}
	live := a.Author
	live.Session = "reused-session"
	if SameParticipant(a.Author, live) {
		t.Fatal("reused pane identity accepted")
	}
	file, _ := s.Path(a.ID)
	if err := os.WriteFile(file, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActiveFor(a.Author.Pane); err == nil {
		t.Fatal("unreadable active assignment ignored")
	}
	if _, err := s.Read("../outside"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := os.Stat(filepath.Join(s.directory(), "state.lock")); !os.IsNotExist(err) {
		t.Fatal("own lock survived transition")
	}
}

func TestAssignmentFamilyMetadataDoesNotRestrictParticipants(t *testing.T) {
	_, a := assignmentFixture(t)
	for _, family := range []string{"openai", "unknown", ""} {
		a.Reviewer.Family = family
		if err := validate(a); err != nil {
			t.Fatalf("family=%q: %v", family, err)
		}
	}
	a.Reviewer.Name = a.Author.Name
	if err := validate(a); err == nil {
		t.Fatal("same participant accepted")
	}
}
