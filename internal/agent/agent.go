package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/FrexCheat/aether-code/internal/compaction"
	"github.com/FrexCheat/aether-code/internal/conversation"
	"github.com/FrexCheat/aether-code/internal/llm"
	"github.com/FrexCheat/aether-code/internal/permissions"
	"github.com/FrexCheat/aether-code/internal/tools"
)

const maxToolRounds = 50

const deferredReminderTemplate = "以下 MCP 工具处于延迟加载状态，当前上下文中只有名字、没有参数定义：\n" +
	"%s\n\n" +
	"需要使用时，先用 ToolSearch 工具检索（支持 select:名字,名字 精确获取，或关键词搜索）获取完整定义；" +
	"拿到定义后按其说明调用：通常按原名直接调用，定义以文本返回时经 McpCall（server/tool/arguments）调用。"

const deferredMarker = "以下 MCP 工具处于延迟加载状态"

type Compactor interface {
	Prepare(ctx context.Context, notify func(compaction.Progress))
	ForceCompact(ctx context.Context, notify func(compaction.Progress)) (compaction.Result, error)
	RecordUsage(u llm.Usage)
	UsedTokens() int
	ContextWindow() int
	Level() compaction.Level
}

var _ Compactor = (*compaction.Manager)(nil)

type Agent struct {
	client        llm.Client
	registry      *tools.Registry
	checker       *permissions.Checker
	deferredNames func() []string
	compactor     Compactor
}

func New(client llm.Client, registry *tools.Registry, checker *permissions.Checker, deferredNames func() []string, compactor Compactor) *Agent {
	return &Agent{
		client:        client,
		registry:      registry,
		checker:       checker,
		deferredNames: deferredNames,
		compactor:     compactor,
	}
}

func (a *Agent) Checker() *permissions.Checker { return a.checker }

func (a *Agent) Registry() *tools.Registry { return a.registry }

// StartTurn creates one single-consumer turn; the caller owns ctx and must
// cancel it if iteration stops before a terminal event.
func (a *Agent) StartTurn(ctx context.Context, conv *conversation.Manager) *Turn {
	return &Turn{
		agent: a,
		ctx:   ctx,
		conv:  conv,
	}
}

func (a *Agent) injectReminders(conv *conversation.Manager) error {
	if a.deferredNames == nil {
		return nil
	}
	names := a.deferredNames()
	if len(names) == 0 {
		return nil
	}
	_, err := conversation.EnsureReminder(conv, deferredMarker, deferredReminder(names))
	return err
}

// repairToolRefs drops tool_reference names the registry no longer offers on
// this request's copy; the stored history keeps the original references.
func (a *Agent) repairToolRefs(messages []conversation.Message) []conversation.Message {
	schemas := a.registry.Schemas()
	known := make(map[string]struct{}, len(schemas))
	for _, schema := range schemas {
		known[schema.Name] = struct{}{}
	}
	for i := range messages {
		for j := range messages[i].ToolResults {
			refs := messages[i].ToolResults[j].ToolRefs
			if len(refs) == 0 {
				continue
			}
			kept := make([]string, 0, len(refs))
			for _, name := range refs {
				if _, ok := known[name]; ok {
					kept = append(kept, name)
				}
			}
			if len(kept) != len(refs) {
				messages[i].ToolResults[j].ToolRefs = kept
			}
		}
	}
	return messages
}

func (a *Agent) notifyCompaction(send eventSink) func(compaction.Progress) {
	return func(progress compaction.Progress) {
		send(CompactionProgress{Progress: progress})
	}
}

func deferredReminder(names []string) string {
	return fmt.Sprintf(deferredReminderTemplate, strings.Join(names, "\n"))
}
