# Copilot Session: Issues & Improvement Notes

> 来源：实际使用 agent-deck 编排 5 个并行 copilot 会话（bid-sys 项目）时的实测记录。  
> 日期：2026-05-02

---

## 一、会话创建问题

### 1.1 `-c` 参数的行为差异

`-c` 传入方式不同，会导致 agent-deck 识别出不同的 `tool` 类型，行为有本质区别：

| 写法 | 识别 tool 类型 | `-g <group>` 可用？ | 备注 |
|------|--------------|---------------------|------|
| `-c copilot` | `tool:copilot` | ❌ 报 "path does not exist" | group 名被当成路径解析 |
| `-c "copilot --allow-all --model claude-opus-4.6"` | `tool:shell` | ✅（从 path 自动推断） | 带空格的字符串走 shell 路径 |

**根因**：命令字符串带空格时，agent-deck 将其视为 shell 命令（`tool:shell`），单词时才走 `tool:copilot` 专属路径。两条路径的 group 解析逻辑不同。

**当前可用写法**：
```bash
agent-deck add <worktree_path> \
  -t "my-session" \
  -c "copilot --allow-all --model claude-opus-4.6"
  # 不要传 -g，让 agent-deck 从 path 自动推断 group
```

---

### 1.2 `-g <group>` 解析失败

**现象**：从 conductor session（cwd = `~/.agent-deck/conductor/ops`）执行：
```bash
agent-deck add /path/to/worktree -t "title" -c copilot -g "bid-sys"
# Error: path does not exist
```

**根因**：agent-deck 将 `-g` 的值当作相对于当前 cwd 的**路径**去解析，而不是 group 标识符。从 conductor cwd 出发找不到 `bid-sys` 路径，因此报错。

**解决方案**：不传 `-g`，直接传 worktree 的完整绝对路径作为 `<path>`，agent-deck 会从路径的 grandparent 目录名自动推断 group（例如 `.worktrees/` 的父目录 `bid-sys`）。

**建议改进**：`-g` 应优先尝试按 group 名称匹配已有 group，失败再尝试路径解析；或明确区分 `--group-name` 和 `--group-path` 两个 flag。

---

### 1.3 `-extra-arg` 不支持 copilot

**现象**：尝试通过 `-extra-arg` 向 copilot 传递 `--allow-all --model claude-opus-4.6`，不生效。

**根因**：`-extra-arg` 仅对 `-c claude` 路径有效，不支持其他 tool 类型。

**当前绕过方案**：将参数直接内嵌到 `-c` 字符串中：`-c "copilot --allow-all --model claude-opus-4.6"`

**建议改进**：`-extra-arg`（或等价的 `--args`）应对所有 tool 类型生效，或提供 per-tool config block（如 `[tools.copilot]`）。

---

### 1.4 `agent-deck launch` vs `agent-deck add`

**现象**：在 conductor cwd（`~/.agent-deck/conductor/ops`）执行 `agent-deck launch <path> -g "bid-sys"` 时因上述 group 路径解析问题报错。

**解决方案**：改用分步操作：
```bash
agent-deck add <worktree_path> -t "title" -c "copilot --allow-all --model claude-opus-4.6"
agent-deck session start <title>
# 通信靠 tmux send-keys，不能用 session send
```

---

## 二、会话通信问题

### 2.1 `agent-deck session send` 对 copilot 会话无效

**现象**：
```bash
agent-deck session send my-copilot-session "do something"
# 命令返回成功，但 copilot 收不到消息
```

**根因**：copilot CLI 没有 ACP（Agent Communication Protocol）集成。  
根据 [issue #556](https://github.com/asheshgoplani/agent-deck/issues/556)，agent-deck 对 claude/codex/gemini 实现了 hook-based lifecycle tracking（activity detection、session-id capture、`--resume` 等），但 copilot 只是基础进程 spawn，没有这些 hook。

**当前绕过方案**：用 `tmux send-keys` 直接向 copilot TUI 注入按键：
```bash
tmux send-keys -t <tmux_session_name> "your message here" Enter
```

**建议改进**：为 copilot 实现 hook 集成（参考 issue #556 中 owner 的回复），或至少支持 stdin 管道注入。

---

### 2.2 `tmux send-keys` 的 Enter 不被 copilot TUI 处理（关键 blocker）

**现象**：
```bash
tmux send-keys -t sess "Read AGENTS.md and implement all tasks" Enter
# 文字出现在 copilot 输入框，但按下 Enter 后消息不被提交
```

**根因**：copilot TUI 使用类 readline 的交互界面，对来自 tmux 控制模式（control mode）的虚拟 `Enter` key event 处理方式不同于真实键盘输入。

**已知可行的绕过方案**（按可靠性排序）：

1. **用 `\r` 替代 `Enter`**（carriage return）：
   ```bash
   tmux send-keys -t sess "your message" $'\r'
   # 或
   tmux send-keys -t sess "your message"$'\r'
   ```

2. **先 `C-c` 清空输入框，逐字符发送后用 `\r` 提交**：
   ```bash
   tmux send-keys -t sess C-c
   sleep 0.2
   # 逐字符发送（对特殊字符更安全）
   echo "your message" | while IFS= read -rn1 char; do
     tmux send-keys -t sess "$char"
     sleep 0.02
   done
   tmux send-keys -t sess $'\r'
   ```

3. **使用 `/task` 命令前缀**（如 copilot CLI 支持）：
   ```bash
   tmux send-keys -t sess "/task Read AGENTS.md and implement all tasks" $'\r'
   ```

4. **改用非交互模式**（最可靠，但失去持续对话能力）：
   ```bash
   copilot -p "Read AGENTS.md and implement all tasks"
   ```

---

## 三、首次启动问题

### 3.1 新目录下的文件夹访问授权弹窗

**现象**：copilot 在新目录首次启动时，会弹出交互式授权弹窗：
```
Do you want to allow access to this directory?
❯ 1. Yes
  2. Yes, and don't ask again for `<tool>` in this directory
  3. No
```

脚本化场景下，如果不处理这个弹窗，后续所有操作都会阻塞。

**处理方式**：
```bash
# 启动后等待弹窗出现，发送 "2" 选择"记住该目录"
sleep 3
tmux send-keys -t sess "2" Enter
```

---

## 四、模型参数问题

### 4.1 模型名称格式

**有效格式**（在 status bar 中显示为 "Claude Opus 4.6"）：
```bash
-c "copilot --allow-all --model claude-opus-4.6"
```

**无效格式**：`claude-opus-4.5`（当时没有此模型的额度/支持时报错）

**建议**：agent-deck 可以在会话详情中暴露 `model` 字段（目前只能从 tmux status bar 目视确认），便于脚本化验证。

---

## 五、改进建议汇总

| 优先级 | 问题 | 建议 |
|--------|------|------|
| P0 | `session send` 对 copilot 无效 | 实现 copilot hook 集成（stdin 管道或 ACP） |
| P0 | `tmux send-keys` Enter 不提交 | 文档说明用 `\r`；或 agent-deck 内部改用 `\r` |
| P1 | `-g` 解析逻辑混乱 | 区分 group name 和 group path，或先按 name 匹配 |
| P1 | `-extra-arg` 仅限 claude | 扩展到所有 tool 类型，或支持 per-tool config block |
| P2 | 首次启动弹窗无法自动化 | 支持 `--allow-all` 时自动接受目录授权，或提供 `--no-interactive` flag |
| P2 | model 字段不可查询 | 在 `session show --json` 中暴露 `model` 字段 |

---

## 六、参考

- [issue #556: Add support for GitHub Copilot CLI](https://github.com/asheshgoplani/agent-deck/issues/556)
- [GitHub Copilot CLI best practices: configure allowed tools](https://docs.github.com/en/copilot/how-tos/copilot-cli/cli-best-practices#configure-allowed-tools)
