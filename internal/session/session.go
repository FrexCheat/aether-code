// Package session owns the append-only JSONL conversation archive: writing
// records ahead of the in-memory history, replaying them on resume, listing
// and expiring sessions, and the per-session lock and spill directory.
package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/FrexCheat/aether-code/internal/config"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
)

// MaxAge is how long an archive is kept after its last write.
const MaxAge = 30 * 24 * time.Hour

// resumeGap is the silence after which a resume gets a staleness reminder:
// the model should re-verify file contents and command output that predate it.
const resumeGap = 6 * time.Hour

const gapReminder = "距上次会话已过去 %s（上次活动：%s）。期间项目文件可能已被修改：历史对话中的文件内容、代码与命令输出都可能过时，引用前必须重新读取或重新执行验证。"

// ErrNoSession reports that no resumable session exists for the directory.
var ErrNoSession = errors.New("session: no previous session for this directory")

// Session is one conversation archive. It implements conversation.Journal:
// every mutation reaches the file before the manager applies it, so the
// in-memory history never holds a message the archive lacks.
type Session struct {
	mu         sync.Mutex
	dir        string
	id         string
	path       string
	lockPath   string
	cwd        string
	started    time.Time
	file       *os.File
	header     bool
	log        []conversation.Message
	boundaries map[string]struct{}
	conv       *conversation.Manager
	writeErr   error
}

// New starts an empty session for cwd: a fresh id, the lock taken and the
// journal attached. No file exists until the first message record is written.
func New(conv *conversation.Manager, cwd string) (*Session, error) {
	dir, err := sessionsDir()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	id, err := newID(now)
	if err != nil {
		return nil, fmt.Errorf("session: cannot mint an id: %w", err)
	}
	s := &Session{
		dir:      dir,
		id:       id,
		path:     filepath.Join(dir, id+".jsonl"),
		lockPath: filepath.Join(dir, id+".lock"),
		cwd:      cwd,
		started:  now,
		conv:     conv,
	}
	if err := acquireLock(s.lockPath); err != nil {
		return nil, err
	}
	conv.SetJournal(s)
	return s, nil
}

// Continue reopens the most recent resumable session for cwd: one that holds
// at least one user message (a run that never got past the injected context
// leaves an empty archive) and whose lock is free.
func Continue(conv *conversation.Manager, cwd string) (*Session, Info, error) {
	infos, err := List(cwd)
	if err != nil {
		return nil, Info{}, err
	}
	for _, info := range infos {
		if info.UserMessages == 0 {
			continue
		}
		s, full, err := resume(conv, info.ID)
		if errors.Is(err, ErrInUse) {
			continue
		}
		if err != nil {
			return nil, Info{}, err
		}
		return s, full, nil
	}
	return nil, Info{}, ErrNoSession
}

// Resume reopens one session by id, wherever it was recorded.
func Resume(conv *conversation.Manager, id string) (*Session, Info, error) {
	return resume(conv, id)
}

// resume locks first, then replays, then hands the history to the manager
// before the journal attaches. Assistant turns whose tool calls all lack
// results are dropped here: nothing of the turn ever ran, so the resumed model
// should not meet a call it never got an answer to. Partly resolved turns
// stay — the request copy repairs those. The count is reported so the UI can
// say what happened.
func resume(conv *conversation.Manager, id string) (*Session, Info, error) {
	if err := sanitizeID(id); err != nil {
		return nil, Info{}, err
	}
	dir, err := sessionsDir()
	if err != nil {
		return nil, Info{}, err
	}
	path := filepath.Join(dir, id+".jsonl")
	lockPath := filepath.Join(dir, id+".lock")
	if _, err := os.Stat(path); err != nil {
		return nil, Info{}, fmt.Errorf("session: session %s not found", id)
	}
	if err := acquireLock(lockPath); err != nil {
		return nil, Info{}, err
	}
	attached := false
	defer func() {
		if !attached {
			releaseLock(lockPath)
		}
	}()

	f, err := os.Open(path)
	if err != nil {
		return nil, Info{}, fmt.Errorf("session: cannot read %s: %w", path, err)
	}
	loaded, err := replay(f)
	f.Close()
	if err != nil {
		return nil, Info{}, err
	}
	if loaded.version > Version {
		return nil, Info{}, fmt.Errorf("session: %s uses record version %d; this build reads up to %d",
			id, loaded.version, Version)
	}

	msgs, dropped := filterUnresolvedTurns(loaded.messages)
	conv.Load(msgs)
	usage := llm.Usage{}
	usageMessages := 0
	if loaded.hasUsage {
		usage = llm.Usage{
			InputTokens:         loaded.usage.InputTokens,
			OutputTokens:        loaded.usage.OutputTokens,
			CacheReadTokens:     loaded.usage.CacheReadTokens,
			CacheCreationTokens: loaded.usage.CacheCreationTokens,
		}
		usageMessages = loaded.usage.Messages
	}
	started := loaded.lastActive
	if started.IsZero() {
		started = time.Now()
	}
	s := &Session{
		dir:      dir,
		id:       id,
		path:     path,
		lockPath: lockPath,
		cwd:      loaded.cwd,
		started:  started,
		// A valid header is already in the file; version 0 means the first
		// write was torn, so the next append rewrites one and heals the file.
		header:     loaded.version != 0,
		log:        msgs,
		boundaries: loaded.boundaries,
		conv:       conv,
	}
	conv.SetJournal(s)
	attached = true

	if !loaded.lastActive.IsZero() {
		if gap := time.Since(loaded.lastActive); gap >= resumeGap {
			_ = conv.AddSystemReminder(fmt.Sprintf(gapReminder, humanGap(gap), loaded.lastActive.Format("2006-01-02 15:04")))
		}
	}
	return s, Info{
		ID:            id,
		Title:         loaded.title,
		Cwd:           loaded.cwd,
		Messages:      loaded.messageCount,
		UserMessages:  loaded.userCount,
		Skipped:       loaded.skipped,
		Dropped:       dropped,
		ModTime:       loaded.lastActive,
		Usage:         usage,
		UsageMessages: usageMessages,
	}, nil
}

func (s *Session) ID() string { return s.id }

// IsBoundary reports whether a user message is a compaction boundary rather
// than something the user typed.
func (s *Session) IsBoundary(content string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.boundaries[content]
	return ok
}

// SpillDir is this session's Layer-1 directory. The id scoping keeps the
// <persisted-output> paths valid across a resume and gives the expiry sweep
// one owner per file.
func (s *Session) SpillDir() string {
	return filepath.Join(s.dir, s.id, "tool-results")
}

// Err is the first archive write failure, sticky for reporting only: later
// writes still go through, and a successful one is perfectly consistent.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeErr
}

// Close closes the file and releases the lock; writes are unbuffered, so
// there is nothing to flush.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if s.file != nil {
		err = s.file.Close()
		s.file = nil
	}
	releaseLock(s.lockPath)
	return err
}

// RecordAppend writes one message record. Reminders are session-scoped
// context and never enter the archive.
func (s *Session) RecordAppend(msg conversation.Message) error {
	if conversation.IsReminder(msg) {
		return nil
	}
	line, err := encodeLine(messageRecordOf(msg, time.Now()))
	if err != nil {
		return fmt.Errorf("session: cannot encode a record: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeLocked(line); err != nil {
		return err
	}
	s.log = append(s.log, msg)
	return nil
}

// RecordReplacePrefix writes a compaction boundary. The keep counts only
// messages the file actually holds — the live history splices in reminders
// that never reach it.
func (s *Session) RecordReplacePrefix(summary conversation.Message, keepFrom int) error {
	rec := boundaryRecord{
		Type:    recordBoundary,
		Content: summary.Content,
		Keep:    s.conv.ContentCount(keepFrom),
		Ts:      time.Now().Format(time.RFC3339),
	}
	line, err := encodeLine(rec)
	if err != nil {
		return fmt.Errorf("session: cannot encode a record: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeLocked(line); err != nil {
		return err
	}
	if s.boundaries == nil {
		s.boundaries = map[string]struct{}{}
	}
	s.boundaries[summary.Content] = struct{}{}
	return nil
}

// RecordUsage appends the accounting of the last completed request. Messages
// counts the archived messages it covered, so a resume can measure its
// estimate baseline against exactly that prefix. The record is advisory: a
// failed write latches into the sticky first-failure report, never failing the
// turn the usage came from.
func (s *Session) RecordUsage(u llm.Usage) {
	if u == (llm.Usage{}) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	line, err := encodeLine(usageRecord{
		Type:                recordUsage,
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		CacheReadTokens:     u.CacheReadTokens,
		CacheCreationTokens: u.CacheCreationTokens,
		Messages:            len(s.log),
		Ts:                  time.Now().Format(time.RFC3339),
	})
	if err != nil {
		s.noteErr(fmt.Errorf("session: cannot encode a record: %w", err))
		return
	}
	_ = s.writeLocked(line)
}

// RecordReplaceToolResult freezes the replacement text the model saw for a
// spilled tool result, so a resume replays those bytes instead of re-deriving
// them.
func (s *Session) RecordReplaceToolResult(toolUseID, content string) error {
	rec := updateRecord{
		Type:      recordUpdate,
		ToolUseID: toolUseID,
		Content:   content,
		Ts:        time.Now().Format(time.RFC3339),
	}
	line, err := encodeLine(rec)
	if err != nil {
		return fmt.Errorf("session: cannot encode a record: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeLocked(line); err != nil {
		return err
	}
	for i := range s.log {
		for j := range s.log[i].ToolResults {
			if s.log[i].ToolResults[j].ToolUseID == toolUseID {
				s.log[i].ToolResults[j].Content = content
			}
		}
	}
	return nil
}

// writeLocked appends one record. The header travels with the first record in
// the same write, and the file opens lazily, so a run that never sends a
// message leaves no archive behind. One write per record with no buffering
// and no fsync: a crash can tear at most the line in flight.
func (s *Session) writeLocked(line []byte) error {
	payload := line
	if !s.header {
		header, err := encodeLine(headerRecord{
			Type:      recordSession,
			Version:   Version,
			ID:        s.id,
			Cwd:       s.cwd,
			StartedAt: s.started.Format(time.RFC3339),
		})
		if err != nil {
			return fmt.Errorf("session: cannot encode a record: %w", err)
		}
		payload = append(header, line...)
	}
	if s.file == nil {
		f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return s.noteErr(fmt.Errorf("session: cannot open %s: %w", s.path, err))
		}
		s.file = f
	}
	if _, err := s.file.Write(payload); err != nil {
		return s.noteErr(fmt.Errorf("session: cannot write %s: %w", s.path, err))
	}
	s.header = true
	return nil
}

// noteErr records the sticky first failure; the caller holds the lock.
func (s *Session) noteErr(err error) error {
	if s.writeErr == nil {
		s.writeErr = err
	}
	return err
}

func sessionsDir() (string, error) {
	root := config.DataRoot()
	if root == "" {
		return "", errors.New("session: cannot resolve the user home directory")
	}
	return filepath.Join(root, "sessions"), nil
}

// filterUnresolvedTurns drops assistant messages whose tool calls all lack
// results, plus the count of dropped turns. Partly resolved messages stay:
// the request copy repairs those.
func filterUnresolvedTurns(msgs []conversation.Message) ([]conversation.Message, int) {
	resolved := make(map[string]struct{})
	for _, m := range msgs {
		for _, tr := range m.ToolResults {
			resolved[tr.ToolUseID] = struct{}{}
		}
	}
	out := make([]conversation.Message, 0, len(msgs))
	dropped := 0
	for _, m := range msgs {
		if m.Role == conversation.RoleAssistant && len(m.ToolUses) > 0 {
			unresolved := true
			for _, tu := range m.ToolUses {
				if _, ok := resolved[tu.ID]; ok {
					unresolved = false
					break
				}
			}
			if unresolved {
				dropped++
				continue
			}
		}
		out = append(out, m)
	}
	return out, dropped
}

func humanGap(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%d 天", int(d.Hours())/24)
	case d >= time.Hour:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	}
}
