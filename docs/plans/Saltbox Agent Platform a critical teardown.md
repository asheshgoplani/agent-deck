# Saltbox Agent Platform: a critical teardown

**Saltbox Hub occupies a genuinely unique niche — no single competitor combines true on-prem self-hosting, Docker container isolation, multi-agent orchestration, and mobile access — but several critical security flaws, a single-developer dependency, and at least two architectural showstoppers threaten its viability.** The most urgent finding: if agent containers are granted NET_ADMIN capability, the entire iptables firewall whitelist can be flushed with a single command, rendering network isolation meaningless. Meanwhile, Coder Tasks + Coder Mux now covers roughly 80% of Saltbox's feature set with enterprise-grade backing, and Claude Code's documented memory leaks (up to **120GB per instance**) make running 5–10 agents on a 64GB NUC unreliable without aggressive containment. This analysis examines four dimensions: competition, security, architecture, and missing capabilities.

---

## Coder Tasks and Mux are now the platform to beat

The competitive landscape shifted dramatically in late 2025. **Coder Tasks**, launched December 2025, provides a browser-based interface for running and managing coding agents (Claude Code, Aider, Amazon Q) in governed, container-isolated workspaces on self-hosted infrastructure. **Coder Mux**, open-sourced under AGPL-3.0 in November 2025, adds a desktop and web app for parallel agentic development with 1,624 commits and growing. Together, they deliver container isolation (Terraform-provisioned workspaces), multi-agent orchestration with dashboards, enterprise governance features (audit logging, Agent Boundaries process-level firewalls, AI Bridge centralized LLM gateway), and self-hosted deployment on any infrastructure — Kubernetes, cloud, or on-prem. Mobile support is the primary gap, though Coder Mux is actively exploring mobile platforms.

**OpenHands** (formerly OpenDevin) is the strongest open-source alternative, with **38,800+ GitHub stars**, $18.8M in seed funding, Docker-based sandboxed execution environments, hierarchical multi-agent delegation, and a React web GUI. It supports self-hosting via Docker or Kubernetes and works with any LLM. Its agent architecture is more sophisticated than a tmux-based approach, though it lacks a mobile-first interface and its dashboard focuses on autonomous agents rather than multi-agent orchestration.

**Ona** (formerly Gitpod, rebranded September 2025) pivoted to become "mission control for software engineering agents," offering sandboxed ephemeral environments, parallel agents, and claimed phone accessibility. However, self-hosting is limited to AWS VPC deployment — not arbitrary on-prem hardware like a NUC.

**Daytona.io** raised **$24M Series A** in February 2026 and pivoted from dev environments to AI agent sandbox infrastructure, reaching $1M ARR in under three months. Its programmable sandboxes (sub-90ms startup, forkable, snapshotable) with OCI/Docker compatibility and Kubernetes deployment could serve as the isolation layer for platforms like Saltbox, though Daytona provides infrastructure rather than a complete orchestration UI.

Among terminal multiplexers, the ecosystem is exploding. **Workmux** stands out as the only tmux-based tool with optional Docker/Lima VM sandboxing per worktree. **agtx** offers a kanban-style TUI for task management. **Claude Squad** (v1.0.10) remains popular for parallel sessions but lacks container isolation. None offer web UIs or mobile access.

The key competitive takeaway: **Saltbox's unique combination remains unduplicated, but the window is narrowing.** Coder's enterprise-backed solution covers most use cases for teams with Kubernetes. Saltbox's differentiation lies in the NUC-deployable simplicity, the mobile-first interface, and avoiding Kubernetes complexity — a "personal DevOps" pitch that no enterprise tool addresses.

---

## The NET_ADMIN capability makes the firewall theater

The security architecture has a critical flaw at its center. Docker containers with **NET_ADMIN capability can modify their own network namespace's iptables rules**. If the iptables whitelist is applied inside the container namespace (which is the standard approach for per-container firewalling), an AI agent running with `--dangerously-skip-permissions` can execute `iptables -F` to flush all rules or `iptables -P OUTPUT ACCEPT` to open all egress — either via direct command or through prompt injection. The kernel checks for CAP_NET_ADMIN in the owning user namespace of the network namespace, and if the container has it at runtime, the rules are modifiable.

This is not theoretical. Claude Code with `--dangerously-skip-permissions` executes arbitrary shell commands without confirmation. A malicious repository containing a crafted `CLAUDE.md` file, a `.cursorrules` directive, or even carefully constructed code comments could trigger this via prompt injection — ranked the **#1 LLM risk by OWASP 2025**. The attack chain is: inject prompt → agent runs `iptables -F` → all egress restrictions removed → exfiltrate SSH keys, API tokens, or establish a reverse shell.

Three **runc container escape vulnerabilities** disclosed in November 2025 (CVE-2025-31133, CVE-2025-52565, CVE-2025-52881, CVSS ~7.3) compound this risk. These exploit race conditions in mount handling to achieve read-write access to host procfs files, **bypassing both AppArmor and SELinux**. Any unpatched Linux Docker host running runc below v1.2.8 is vulnerable.

The **SSH key mounting pattern** is another high-risk vector. Even read-only mounts expose the full private key material — `cat /root/.ssh/id_rsa` works regardless of the read-only flag. If any network egress exists (even to whitelisted hosts like GitHub), the agent can exfiltrate keys by committing them to a public repository or encoding them in DNS query subdomains. SSH agent forwarding should replace direct key mounting.

**Domain-based iptables whitelisting** is fundamentally fragile. DNS resolution is point-in-time; CDN providers rotate IPs constantly. DNS rebinding attacks can force re-resolution to internal IPs. And even with perfect IP whitelisting, DNS queries themselves serve as an exfiltration channel — data encoded in subdomain labels (`secret-data.attacker.com`) bypasses all IP-level controls. An **HTTP/HTTPS proxy** (e.g., Squid) inspecting Host headers/SNI is far more robust than IP-based rules.

The **tecnativa/docker-socket-proxy** provides meaningful protection over raw socket mounting but has significant limitations: coarse-grained category-level filtering (no per-container ACLs), no authentication or TLS, and if CONTAINERS + POST are enabled (required for Saltbox to spawn agent containers), any compromised container on the proxy's network can create privileged sibling containers with host filesystem mounts.

| Security area | Risk | Key concern |
|---|---|---|
| NET_ADMIN + iptables | **Critical** | Agent can flush its own firewall rules |
| `--dangerously-skip-permissions` | **Critical** | All safety bypassed; security = container boundary |
| Prompt injection → code execution | **Critical** | Attacker-controlled actions via crafted repos |
| runc container escape (Nov 2025) | **High** | Three CVEs bypass AppArmor/SELinux |
| SSH key exposure | **High** | Read-only mount still allows full key exfiltration |
| Docker socket proxy | **Medium-High** | Coarse filtering; sibling container creation possible |
| Domain iptables whitelisting | **Medium-High** | DNS rebinding, CDN rotation, DNS exfiltration |

---

## Two architectural showstoppers: tmux scrollback and Agent Deck

The choice of `tmux capture-pane` for terminal streaming has a **confirmed showstopper bug**: Claude Code's `autocompact`/`compact` operations clear the entire tmux scrollback buffer (GitHub issue #16310). When this happens, the Saltbox platform loses all historical output — the web UI goes blank or shows stale data. Beyond this specific bug, `capture-pane` is fundamentally a polling mechanism, not a stream. Each call takes a point-in-time snapshot; to approach real-time updates, the platform must poll every 100–500ms. During high-throughput operations, output between polls is lost forever once the scrollback buffer fills (default: 2,000 lines). The alternative is `tmux pipe-pane`, which provides a true output stream, or using a Go pty library (`github.com/creack/pty`) to capture output directly.

The **Agent Deck dependency** carries the worst possible bus factor: **1 developer, 8 GitHub stars, 84 commits**. This is a personal project being used as core infrastructure. If the maintainer loses interest, changes careers, or simply takes a long vacation, Saltbox loses its session management capability with no community to pick up maintenance. The functionality Agent Deck provides — tmux session management plus status detection — is implementable in roughly 1–2 weeks of Go development. An immediate fork or in-house replacement should be a priority.

**Filesystem JSON persistence** breaks down exactly where Saltbox needs it most: concurrent access. JSON files are fundamentally single-writer. Multiple agents reading and writing the same task state file create race conditions — partial writes, torn reads, and corruption. There is no locking, no transaction support, and no crash recovery. At modest scale (1,000+ tasks), the entire file must be parsed for any query. **SQLite** is the clear migration target: embedded, ACID-compliant, concurrent-reader-friendly in WAL mode, and native JSON support since v3.45.

**SSE for terminal streaming** is actually well-suited to this use case — terminal output is unidirectional, and SSE has built-in auto-reconnection with `Last-Event-ID`. The critical requirement is **HTTP/2**: under HTTP/1.1, browsers limit SSE to 6 concurrent connections per domain across all tabs, which is a hard blocker for a 5–10 agent dashboard. With HTTP/2 (which requires TLS), this limit rises to ~100 multiplexed streams.

**htmx** is reasonable for v1 of an internal tool but becomes a liability if the UI grows. The Gumroad team publicly abandoned htmx for React citing difficulties with workflow builders and dynamic interfaces. For Saltbox, the core terminal display works, but features like drag-to-resize panels, agent workflow configuration, or dependency graphs would be painful. A sensible escape hatch: structure server endpoints to return both JSON and HTML fragments via content negotiation, enabling a future frontend rewrite without touching the backend.

---

## Claude Code on a NUC: memory leaks meet physics

Running multiple Claude Code instances on a **32–64GB NUC** faces a hard physical constraint compounded by software bugs. Reported per-instance memory usage ranges from a baseline of 1–2GB to observed figures of **2.5–8GB during normal operation**. Multiple confirmed memory leak bugs are far worse: GitHub issues document instances growing to **30GB** (issue #9711), **120GB** (issue #4953), and **129GB virtual memory** (issue #11315) before OOM kills or system freezes. One user reported 80–90% CPU utilization from a single instance during intensive tasks.

On a 64GB NUC with an Intel i7 (typically 4–6 cores, 8–12 threads), realistic concurrency is **3–5 agents** with enforced Docker memory limits of 4–6GB each, reserving 8–10GB for the OS, Docker daemon, tmux, and the Go platform itself. Without Docker memory limits (`--memory=4g --memory-swap=6g`), a single runaway instance can consume all available RAM and crash the host.

**API costs** scale linearly with concurrency. At Anthropic's current pricing (Sonnet 4.5: $3/$15 per million input/output tokens), the baseline is **$6/agent/day** average, $12 at the 90th percentile. Five concurrent agents cost **$900–1,800/month**; ten agents run **$1,800–3,600/month**. Agent-teams mode (multiple sub-agents) uses approximately **7× more tokens**, pushing costs to $6,300–12,600/month. Rate limits compound the problem — at Tier 2 (1,000 RPM shared across all keys in an organization), 10 concurrent agents will hit throttling within seconds during burst activity. Tier 3 minimum ($200 deposit, 2,000 RPM) is necessary for 5+ agents; Tier 4 for 10+.

---

## Six missing capabilities that mature platforms consider table stakes

The most impactful architectural change Saltbox could make is **routing all agent API calls through the Go backend as a centralized proxy**. This single chokepoint enables cost tracking, rate limiting, audit logging, budget enforcement, and observability — addressing five of the six critical gaps simultaneously.

**Cost tracking** is the highest-priority gap. Without it, a single runaway agent loop can burn thousands of dollars in hours with zero warning. Mature platforms provide real-time token-level tracking with per-agent attribution, budget thresholds with graduated alerts (50%/80%/100%), and spend forecasting. **Langfuse** (MIT license, self-hostable, 19K+ GitHub stars) is the ideal fit for a NUC deployment — it provides cost tracking, distributed tracing, and prompt management in a single Docker-based tool.

**Backup and disaster recovery** is equally critical. A single NUC is a single point of failure for everything — the Go binary, Docker containers, database, configuration, agent state, and logs. When the NUC dies from hardware failure, power surge, or disk failure, recovery time is currently undefined. The minimum viable strategy: automated offsite backups via `restic` to Backblaze B2 (implementable in an hour), infrastructure-as-code so the NUC is reproducible from a Git repository, and a documented recovery runbook with defined RPO/RTO targets.

**Secret management** via environment variables is a known anti-pattern. Environment variables are visible in `/proc/<pid>/environ`, exposed by `docker inspect`, inherited by all child processes, and frequently leaked in logs. Docker Compose secrets (mounted as files at `/run/secrets/`) require minimal code changes and eliminate the most dangerous exposure vectors. For encryption at rest, Mozilla SOPS with age keys is lightweight and requires no additional services.

**Rate limiting** without coordination means multiple agents independently exhausting shared API quotas, causing unpredictable 429 errors and wasted tokens on retries. A centralized token bucket in the Go backend, tracking Anthropic's `anthropic-ratelimit-*` response headers, with priority queuing for critical agents, prevents this.

**Audit logging** matters not just for compliance but for debugging. Every agent action — file operations, shell commands, git operations, API calls — should be logged in structured JSON with agent_id, session_id, timestamps, and inputs/outputs. Without this, answering "what did the agent do at 3am" is impossible.

---

## Conclusion: viable but fragile, with a narrowing competitive window

Saltbox Hub's core thesis — a personal, NUC-deployable alternative to enterprise Kubernetes-based platforms — remains valid and underserved. No competitor delivers the full combination of true on-prem simplicity, container isolation, multi-agent orchestration, and mobile access. But this analysis reveals that the platform's security model has a critical hole (NET_ADMIN negates the firewall), its core streaming mechanism has a confirmed showstopper (Claude Code clears tmux scrollback), and its most important dependency has a bus factor of one.

The five highest-priority actions, in order:

1. **Remove NET_ADMIN** from agent containers immediately and apply iptables rules in the host namespace (DOCKER-USER chain), not inside containers
2. **Replace tmux capture-pane** with `pipe-pane` or direct pty capture to eliminate polling latency and the scrollback-clearing bug
3. **Fork or replace Agent Deck** — 8 stars and a single maintainer is not infrastructure
4. **Add Docker memory limits** (4–6GB per container) with automatic session recycling every 2–4 hours to contain Claude Code's memory leaks
5. **Deploy a centralized API proxy** in the Go backend for cost tracking, rate limiting, audit logging, and budget enforcement in one layer

The competitive threat from Coder Tasks + Mux is real but not immediate — Coder targets teams with Kubernetes expertise, while Saltbox targets individual developers who want a plug-and-play NUC. The more existential risk is that OpenHands, Daytona, or Ona builds mobile access before Saltbox achieves production stability. Speed matters, but shipping the five fixes above matters more — a platform that can't contain its own agents isn't ready for users.