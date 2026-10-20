package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/skills"
)

// Version is the on-disk record schema version; a file written by a newer
// version is refused rather than misread. Version 2 added thinking blocks to
// message records; version 1 files replay as before, minus that state.
const Version = 2

const (
	recordSession  = "session"
	recordMessage  = "message"
	recordBoundary = "boundary"
	recordUpdate   = "update"
)

// The record types are session-owned rather than conversation.Message: the
// file format is a versioned contract of its own and must not drift with the
// in-memory type. Reminders are deliberately absent — they are session-scoped
// context that a resume rebuilds instead of replays.

type headerRecord struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	ID        string `json:"id"`
	Cwd       string `json:"cwd"`
	StartedAt string `json:"started_at"`
}

// thinkingRecord is one reasoning block. Both APIs require the model's own
// reasoning back on the next request, so a resumed session that replays a tool
// turn without it is rejected by thinking-mode endpoints; the block is
// archived for exactly that replay.
type thinkingRecord struct {
	Text      string `json:"text,omitempty"`
	Signature string `json:"signature,omitempty"`
	Content   string `json:"content,omitempty"`
	Encrypted string `json:"encrypted,omitempty"`
}

type toolUseRecord struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type toolResultRecord struct {
	ToolUseID string   `json:"tool_use_id"`
	Content   string   `json:"content"`
	IsError   bool     `json:"is_error,omitempty"`
	ToolRefs  []string `json:"tool_refs,omitempty"`
}

type messageRecord struct {
	Type        string             `json:"type"`
	Role        string             `json:"role"`
	Content     string             `json:"content,omitempty"`
	Thinking    []thinkingRecord   `json:"thinking,omitempty"`
	ToolUses    []toolUseRecord    `json:"tool_uses,omitempty"`
	ToolResults []toolResultRecord `json:"tool_results,omitempty"`
	Ts          string             `json:"ts"`
}

type boundaryRecord struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Keep    int    `json:"keep"`
	Ts      string `json:"ts"`
}

type updateRecord struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	Ts        string `json:"ts"`
}

func messageRecordOf(msg conversation.Message, now time.Time) messageRecord {
	rec := messageRecord{
		Type:    recordMessage,
		Role:    string(msg.Role),
		Content: msg.Content,
		Ts:      now.Format(time.RFC3339),
	}
	for _, tb := range msg.ThinkingBlocks {
		rec.Thinking = append(rec.Thinking, thinkingRecord{
			Text:      tb.Text,
			Signature: tb.Signature,
			Content:   tb.Content,
			Encrypted: tb.Encrypted,
		})
	}
	for _, tu := range msg.ToolUses {
		rec.ToolUses = append(rec.ToolUses, toolUseRecord{ID: tu.ID, Name: tu.Name, Args: tu.Args})
	}
	for _, tr := range msg.ToolResults {
		rec.ToolResults = append(rec.ToolResults, toolResultRecord{
			ToolUseID: tr.ToolUseID,
			Content:   tr.Content,
			IsError:   tr.IsError,
			ToolRefs:  tr.ToolRefs,
		})
	}
	return rec
}

func (r messageRecord) toMessage() (conversation.Message, bool) {
	role := conversation.Role(r.Role)
	if role != conversation.RoleUser && role != conversation.RoleAssistant {
		return conversation.Message{}, false
	}
	msg := conversation.Message{Role: role, Content: r.Content}
	for _, tb := range r.Thinking {
		msg.ThinkingBlocks = append(msg.ThinkingBlocks, conversation.ThinkingBlock{
			Text:      tb.Text,
			Signature: tb.Signature,
			Content:   tb.Content,
			Encrypted: tb.Encrypted,
		})
	}
	for _, tu := range r.ToolUses {
		msg.ToolUses = append(msg.ToolUses, conversation.ToolUseBlock{ID: tu.ID, Name: tu.Name, Args: tu.Args})
	}
	for _, tr := range r.ToolResults {
		msg.ToolResults = append(msg.ToolResults, conversation.ToolResultBlock{
			ToolUseID: tr.ToolUseID,
			Content:   tr.Content,
			IsError:   tr.IsError,
			ToolRefs:  tr.ToolRefs,
		})
	}
	if msg.Content == "" && len(msg.ThinkingBlocks) == 0 && len(msg.ToolUses) == 0 && len(msg.ToolResults) == 0 {
		return conversation.Message{}, false
	}
	return msg, true
}

// loaded is one file replayed from top to bottom.
type loaded struct {
	version      int
	id           string
	cwd          string
	messages     []conversation.Message
	messageCount int // message records seen, boundary or not
	userCount    int // user text records seen — the title/emptiness probe
	title        string
	boundaries   map[string]struct{}
	lastActive   time.Time
	skipped      int
}

// replay materializes the records in file order. It never fails on a bad
// line: a torn or corrupt record is skipped and counted, which is exactly
// what makes a crash lose at most the write in flight. A torn boundary only
// means the pre-boundary history replays in full — more context, never less.
func replay(r io.Reader) (loaded, error) {
	var out loaded
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			out.consume(line)
		}
		if err != nil {
			if err == io.EOF {
				return out, nil
			}
			return out, fmt.Errorf("session: cannot read the archive: %w", err)
		}
	}
}

func (l *loaded) consume(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		l.skipped++
		return
	}
	switch probe.Type {
	case recordSession:
		var rec headerRecord
		if json.Unmarshal(line, &rec) != nil {
			l.skipped++
			return
		}
		l.version = rec.Version
		l.id = rec.ID
		l.cwd = rec.Cwd
		l.touch(rec.StartedAt)
	case recordMessage:
		var rec messageRecord
		if json.Unmarshal(line, &rec) != nil {
			l.skipped++
			return
		}
		msg, ok := rec.toMessage()
		if !ok {
			l.skipped++
			return
		}
		l.messageCount++
		if msg.Role == conversation.RoleUser && msg.Content != "" {
			l.userCount++
			// A delivered Skill block is not something the user said, so it is
			// no title for the session either; the request that came with it is.
			if l.title == "" && !skills.IsBlock(msg.Content) {
				l.title = makeTitle(msg.Content)
			}
		}
		l.messages = append(l.messages, msg)
		l.touch(rec.Ts)
	case recordBoundary:
		var rec boundaryRecord
		if json.Unmarshal(line, &rec) != nil {
			l.skipped++
			return
		}
		keep := min(max(rec.Keep, 0), len(l.messages))
		tail := l.messages[len(l.messages)-keep:]
		l.messages = append([]conversation.Message{{Role: conversation.RoleUser, Content: rec.Content}}, tail...)
		if l.boundaries == nil {
			l.boundaries = map[string]struct{}{}
		}
		l.boundaries[rec.Content] = struct{}{}
		l.touch(rec.Ts)
	case recordUpdate:
		var rec updateRecord
		if json.Unmarshal(line, &rec) != nil {
			l.skipped++
			return
		}
		for i := range l.messages {
			for j := range l.messages[i].ToolResults {
				if l.messages[i].ToolResults[j].ToolUseID == rec.ToolUseID {
					l.messages[i].ToolResults[j].Content = rec.Content
				}
			}
		}
		l.touch(rec.Ts)
	default:
		// Unknown types are ignored so a newer minor format stays readable.
	}
}

func (l *loaded) touch(ts string) {
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		l.lastActive = t
	}
}

// makeTitle derives the one-line session title from a user message.
func makeTitle(content string) string {
	line := content
	if i := bytes.IndexAny([]byte(line), "\r\n"); i >= 0 {
		line = line[:i]
	}
	runes := []rune(line)
	if len(runes) > 60 {
		return string(runes[:60]) + "…"
	}
	return string(runes)
}

// newID mints YYYYMMDD-HHMMSS-xxxx with four random hex characters so two
// sessions in the same second cannot collide.
func newID(now time.Time) (string, error) {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return now.Format("20060102-150405") + "-" + hex.EncodeToString(b[:]), nil
}

func encodeLine(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}
