package compaction

import "fmt"

// spillMarker opens every spill file; its presence anywhere in a tool result
// means the text was read back from a spill file (Read line numbers, cat,
// grep hits all carry it mid-content), so matching must use Contains.
const spillMarker = "aethercode spilled tool result"

const persistedOpenTag = "<persisted-output>"

const summaryPrompt = `严格禁止调用任何工具：不要调用任何工具，工具调用会被直接拒绝，并浪费你唯一的一次回复机会。你只能输出文本。

下面是一次编码会话的对话历史。由于即将超出上下文窗口，其中较早的消息将被丢弃，你的任务是输出一份结构化摘要，让后续对话能够无缝继续。逐条完成以下小节，全部使用中文（代码、命令、路径、标识符保持原样）：

1. 主要请求与意图：用户想让助手做什么？逐条列出意图与目标，包括后续的修正与补充。
2. 关键技术概念：涉及的技术、库、框架、协议与约定，各附一句说明。
3. 文件与代码段：列出创建或修改过的每个文件及其路径、改动要点；关键代码用代码块原样保留（含 import、函数签名与关键行），不得凭记忆改写。
4. 错误与修复：列出遇到过的错误、报错要点、根因与最终修法；注明尚未解决的错误。
5. 问题求解：为解决开放问题尝试过什么、哪些路线可行、哪些已被排除。
6. 所有用户消息：按顺序完整逐字引用每一条用户消息，不得改写、不得概括、不得省略任何一条；<system-reminder> 包裹的系统信息不算用户消息。
7. 待办事项：用户已提出但尚未完成的事项，注明哪些已完成。
8. 当前工作：截断时刻正在做什么，精确到最后一步操作与相关文件的当前状态。
9. 下一步（可选）：仅当对话中已有明确的下一步时，直接引用用户或助手的原话说明；否则省略本节。

输出格式（严格遵守）：先输出 <analysis> 标签，在其中起草上述各节并自查完整性（该草稿随后会被丢弃）；确认无误后输出 <summary> 标签，给出最终摘要正文。<summary> 结束后不得再有任何文字。再次强调：全程禁止任何工具调用。`

func spillFileHeader(toolUseID string, chars int) string {
	return fmt.Sprintf(
		"%s：一次工具调用的完整输出，因超出大小阈值未保留在会话上下文中。\n工具调用 ID: %s\n字符数: %d\n\n",
		spillMarker, toolUseID, chars)
}

func persistedReplacement(chars int, path, preview string) string {
	return fmt.Sprintf(`%s
该工具输出过大（%d 字符），未保留在上下文中，已完整写入文件：
%s

前 %d 字符预览：
%s
需要完整内容时，用 Read 工具读取上述文件（可配合 offset/limit 分段读取）。
</persisted-output>`, persistedOpenTag, chars, path, len([]rune(preview)), preview)
}
