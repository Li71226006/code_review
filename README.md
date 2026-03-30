# CodeSentry

<div align="center">
  <img src="https://raw.githubusercontent.com/huangang/codesentry/main/frontend/public/codesentry-icon.png" alt="CodeSentry Logo" width="120" height="120">
</div>

> **声明 / Disclaimer**: 
> 本项目为基于 [huangang/codesentry](https://github.com/huangang/codesentry) 二次开发的分支版本，主要用于**学习、教育及架构研究用途**。
> 感谢原作者 [huangang](https://github.com/huangang) 提供的优秀开源基础。

AI-powered Code Review Platform for GitHub, GitLab, and Bitbucket. 
具备双引擎 (V1/V2) 智能上下文解析与超大 PR 自动分批审查能力的专业级代码审查系统。

[中文文档](./README_zh.md)

## 核心架构与操作流程 (Operation Flow)

本系统在处理代码审查（支持 Push 和 Merge Request 事件）时，采用多级防护与智能解析流水线：

1. **Webhook 触发**: 接收来自 GitLab/GitHub 的 Push 或 MR 事件，提取变更信息。
2. **前置过滤 (Max Files / Max File Size)**:
   - 按照代码改动量对所有被修改的文件进行降序排列，仅保留前 `N` 个核心文件（可配置，防止内存溢出）。
   - 拉取文件源码时，自动跳过超过 `Max File Size` 的超大文件。
3. **双引擎上下文解析 (V1 / V2)**:
   - 根据配置，系统将过滤后的文件送入 V1 或 V2 引擎进行上下文组装（详见下文引擎说明）。
4. **智能分批审查 (Chunking)**:
   - 在发送给 LLM 之前，检查组装好的 Prompt 总 Token/字符数。
   - 如果超过设定阈值（如 50,000 字符），系统会**以文件为最小粒度**，将 PR 拆分为多个 Batch。
   - **并发请求**多个 AI 模型实例，确保无论多大的提交都不会被截断。
5. **聚合输出**: 将各批次的 AI 审查结果加权合并，通过 IM (钉钉/飞书/企微等) 通知用户，并在代码托管平台上回复 Comment。

## 双引擎解析逻辑 (V1 vs V2 Review Modes)

为了在“审查精度”与“Token 成本/响应速度”之间取得最佳平衡，系统内置了两套代码解析引擎：

### V1 模式：轻量级极速审查
- **定位**：适合日常小迭代、前端 UI 微调、配置文件修改，成本极低。
- **核心逻辑**：基于纯文本的智能上下文提取。围绕代码修改点（Diff），自动提取上下 10 行作为上下文。
- **智能合并 (Merge)**：如果一个文件内有多个修改点且距离较近（例如相距不到 20 行），系统会自动将这些修改点及上下文无缝合并为一个连贯的代码块，避免 AI 看到碎片化的代码而产生幻觉。

### V2 模式：专家级深度审查 (AST 语法树)
- **定位**：适合核心业务重构、底层数据结构变更等牵一发而动全身的复杂 PR，主打高精度防雷。
- **完整函数包裹 (Function Context)**：利用 Tree-sitter AST 解析，不仅提取修改的那几行，而是把修改点所在的**整个函数/类**完整提取出来，并使用 `+` 和 `-` 标记具体修改位置。
- **跨文件影响分析 (Callers Context)**：向上追溯。全网扫描**谁调用了被修改的函数**。例如，如果您修改了函数的输入输出参数，AI 能立刻结合 Callers 上下文判断是否会导致其他模块报错。
- **底层规范校验 (Callee Context)**：向下校验。全网扫描**本次修改代码中调用的底层函数**。将底层函数的原始定义拿给 AI 看，确保您传入的参数类型、数量完全符合底层规范。
- **孤儿代码兜底 (Orphan Hunks)**：如果修改的是全局变量、包导入（import）等不属于任何函数/类的顶层代码，V2 引擎会触发兜底机制，自动为其提供上下 5 行的全局上下文，确保没有任何一处修改被遗漏。

## 关键 API 接口设计 (Key APIs)

系统前后端分离，后端采用 Go 编写，提供标准 RESTful API：

- **`/api/webhook/{platform}/{uuid}`**
  核心入口，接收 GitHub/GitLab/Bitbucket 推送的事件，解析 Diff 并触发异步/同步审查队列。
- **`/api/projects`**
  项目管理接口，用于配置代码仓库的鉴权信息、指定使用的 LLM 模型及 Review Mode (V1/V2)。
- **`/api/prompts`**
  提示词管理，支持动态注入 `{{file_context}}`、`{{callers_context}}`、`{{callee_context}}` 等变量。
- **`/api/logs/review`**
  查询历史审查日志，支持分页、关键字检索及批量重试/删除。
- **`/metrics`**
  Prometheus 监控指标接口，实时暴露队列堆积、API 耗时及请求成功率。

## Features

- **AI Code Review**: Native API support for OpenAI, Anthropic (Claude), Ollama, Google Gemini, and Azure OpenAI
- **File Context**: Fetch full file content to provide better context for AI review, reducing false positives
- **Chunked Review**: Automatically splits large MRs/PRs into batches for optimal review quality
- **Smart Filtering**: Auto-skips config files, lock files, and generated files (customizable)
- **Auto-Scoring**: Automatically appends scoring instructions if custom prompts lack them
- **Commit Comments**: Post AI review results as comments on commits (GitLab/GitHub)
- **Commit Status**: Set commit status to block merges when score is below threshold (GitLab/GitHub)
- **Sync Review API**: Synchronous review endpoint for Git pre-receive hooks to block pushes
- **Duplicate Prevention**: Skip already reviewed commits to avoid redundant processing
- **Multi-Platform Support**: GitHub, GitLab, and Bitbucket webhook integration with multi-level project path support
- **Dashboard**: Visual statistics and metrics for code review activities
- **Real-time Updates**: SSE-powered live status updates (pending → analyzing → completed) without page refresh
- **Review History**: Track all code reviews with detailed logs and direct links to commits/MRs
- **Project Management**: Manage multiple repositories

## Preview

![CodeSentry Dashboard](https://raw.githubusercontent.com/huangang/codesentry/main/frontend/public/dashboard-preview.png)

- **LLM Configuration**: Configure multiple AI models with native SDK integration (no proxy required for Anthropic/Gemini)
- **Prompt Templates**: System and custom prompt templates with copy functionality
- **IM Notifications**: Send review results to DingTalk, Feishu, WeCom, Slack, Discord, Microsoft Teams, Telegram
- **Daily Reports**: Automated daily code review summary with AI analysis, sent via IM bots
- **Error Notifications**: Real-time error alerts via IM bots
- **Git Credentials**: Auto-create projects from webhooks with credential management
- **System Logging**: Comprehensive logging for webhook events, errors, and system operations
- **Authentication**: Local authentication and LDAP support (configurable via web UI)
- **Role-based Access Control**: Admin, Developer, and User roles with granular permissions
- **Multi-Database**: SQLite for development, MySQL/PostgreSQL for production
- **Async Task Queue**: Optional Redis-based async processing for AI reviews (graceful fallback to sync mode)
- **Internationalization**: Support for English and Chinese (including DatePicker localization)
- **Responsive Design**: Mobile-friendly interface with adaptive layouts for phones and tablets
- **Dark Mode**: Toggle between light and dark themes, with preference persistence
- **Global Search**: Cross-project search for reviews and projects from the header
- **Multi-Language Review Prompts**: Auto-detect programming language from diffs and inject language-specific review guidelines (Go, Python, JS/TS, Java, Rust, Ruby, PHP, Swift, Kotlin, C/C++)
- **Batch Operations**: Batch retry and batch delete for review logs
- **Real-time Notifications**: SSE-powered notification bell with unread badge and live review events
- **Reports**: Weekly/monthly report API with period comparison, daily trends, and author rankings
- **Issue Tracker Integration**: Auto-create Jira, Linear, GitHub Issues, or GitLab Issues when review score is below threshold
- **Auto-Fix PR**: AI-generated code fixes — automatically creates branch, commits patches, and opens PR (GitHub) or MR (GitLab)
- **Rule Engine**: Automated CI/CD policies with conditions (score_below, files_changed_above, has_keyword) and actions (block, warn, notify)
- **Prometheus Metrics**: `/metrics` endpoint for monitoring
- **Audit Logging**: Automatic audit logging for all admin write operations
- **Review Diff Cache**: SHA-256 hash deduplication to skip already-reviewed diffs
- **CSV Export**: Export review logs as CSV for offline analysis
- **Manual Score Override**: Admin can manually override AI scores with reason tracking and original score preservation

## Quick Start

### Prerequisites

- Go 1.24+
- Node.js 20+
- Docker (optional)

### Development Setup

#### Backend

```bash
cd backend

# Create config file
cp ../config.yaml.example config.yaml
# Edit config.yaml with your settings

# Run
go run ./cmd/server
```

#### Frontend

```bash
cd frontend

# Install dependencies
npm install

# Run development server
npm run dev
```

Access the application at `http://localhost:5173`

**Default credentials**: `admin` / `admin`

### Docker Deployment

```bash
# Pull from Docker Hub
docker pull huangangzhang/codesentry:latest

# Or pull from GitHub Container Registry
docker pull ghcr.io/huangang/codesentry:latest
```

**Choose your database:**

```bash
# MySQL (default, recommended for production)
docker-compose up -d

# SQLite (simple, single file)
docker-compose -f docker-compose.sqlite.yml up -d

# PostgreSQL
docker-compose -f docker-compose.postgres.yml up -d
```

**Or run directly (SQLite):**

```bash
docker run -d -p 8080:8080 -v codesentry-data:/app/data huangangzhang/codesentry:latest
```

For local development (build from source):

```bash
docker-compose -f docker-compose.dev.yml up --build
```

Access the application at `http://localhost:8080`

### Build Script (Local)

```bash
# One-command build (frontend + backend combined)
./build.sh

# Run the binary
./codesentry
```

This builds frontend, embeds it into the Go binary, producing a single executable.

## Configuration

Copy `config.yaml.example` to `config.yaml` and update:

```yaml
server:
  port: 8080
  mode: release  # debug, release, test

database:
  driver: sqlite   # sqlite, mysql, postgres
  dsn: data/codesentry.db
  # For MySQL: user:password@tcp(host:port)/dbname?charset=utf8mb4&parseTime=True&loc=Local
  # For PostgreSQL: host=localhost user=postgres password=xxx dbname=codesentry port=5432 sslmode=disable

jwt:
  secret: your-secret-key-change-in-production
  expire_hour: 24
```

### Session & Token Expiration

CodeSentry uses a **short-lived access token** (JWT) plus a **long-lived refresh token** for silent re-login.

- **Access token**: returned by `POST /api/auth/login`, stored by frontend in `localStorage` and sent as `Authorization: Bearer <token>`.
- **Refresh token**: stored in a **httpOnly cookie** (not accessible from JavaScript), used by `POST /api/auth/refresh`.

Default expirations (configurable via System Config in DB):

- `auth_access_token_expire_hours` (default: `2`)
- `auth_refresh_token_expire_hours` (default: `720` = 30 days)

`jwt.expire_hour` in `config.yaml` is used as a fallback default for access token expiration.

> **Note**: When the access token expires, the frontend will automatically call `/api/auth/refresh` and retry the original request. Only when refresh fails will it redirect to `/login`.

The frontend also performs **proactive refresh** before token expiration (default: refresh 5 minutes before access token expires) to reduce user-visible 401 interruptions.

### CORS (Important for Refresh Cookie)

Because refresh token is stored in a cookie, deployments that serve frontend and backend on different origins must configure CORS correctly:

- `Access-Control-Allow-Credentials: true` is required
- `Access-Control-Allow-Origin` **cannot** be `*`
- Prefer an explicit origin allowlist in production

> **Note**: All business configurations (LLM models, LDAP, prompts, IM bots, Git credentials) are managed via the web UI and stored in the database.

## Webhook Setup

### Recommended: Unified Webhook (Auto-detect)

Use a single webhook URL for GitLab, GitHub, and Bitbucket:

```
https://your-domain/webhook
# or
https://your-domain/review/webhook
```

The system automatically detects the platform via request headers.

### GitHub

1. Go to Repository Settings > Webhooks > Add webhook
2. Payload URL: `https://your-domain/webhook`
3. Content type: `application/json`
4. Secret: Your configured webhook secret
5. Events: Select "Pull requests" and "Pushes"

### GitLab

1. Go to Project Settings > Webhooks
2. URL: `https://your-domain/webhook`
3. Secret Token: Your configured webhook secret
4. Trigger: Push events, Merge request events

### Bitbucket

1. Go to Repository Settings > Webhooks > Add webhook
2. URL: `https://your-domain/webhook`
3. Secret: Your configured webhook secret (for HMAC-SHA256 signature)
4. Triggers: Select "Repository push" and "Pull request created/updated"

## API Endpoints

### Authentication

- `POST /api/auth/login` - Login
- `POST /api/auth/refresh` - Refresh access token (uses httpOnly refresh cookie)
- `GET /api/auth/config` - Get auth config
- `GET /api/auth/me` - Get current user
- `POST /api/auth/logout` - Logout (revokes refresh token and clears cookie)
- `POST /api/auth/change-password` - Change password (local users only)

### System Config (Admin)

- `GET /api/system-config/auth-session` - Get auth session config
- `PUT /api/system-config/auth-session` - Update auth session config

### Projects

- `GET /api/projects` - List projects
- `POST /api/projects` - Create project
- `GET /api/projects/:id` - Get project
- `PUT /api/projects/:id` - Update project
- `DELETE /api/projects/:id` - Delete project

### Review Logs

- `GET /api/review-logs` - List review logs
- `GET /api/review-logs/:id` - Get review detail
- `POST /api/review-logs/:id/retry` - Retry failed review (admin only)
- `DELETE /api/review-logs/:id` - Delete review log (admin only)

### Real-time Events (SSE)

- `GET /api/events/reviews` - Stream review status updates (requires `token` query param)

### Users

- `GET /api/users` - List users (admin only)
- `PUT /api/users/:id` - Update user (admin only)
- `DELETE /api/users/:id` - Delete user (admin only)

### Dashboard

- `GET /api/dashboard/stats` - Get statistics

### Global Search

- `GET /api/search?q=<query>&limit=<n>` - Search across reviews and projects

### Reports

- `GET /api/reports?period=weekly|monthly&project_id=N` - Period stats with trend and author rankings

### Review Logs

- `GET /api/review-logs` - List review logs (supports score range, status, author, date filters)
- `GET /api/review-logs/:id` - Get review detail
- `GET /api/review-logs/export` - Export review logs as CSV (admin only)
- `POST /api/review-logs/:id/retry` - Retry failed review (admin only)
- `POST /api/review-logs/batch-retry` - Batch retry (admin only)
- `POST /api/review-logs/batch-delete` - Batch delete (admin only)
- `DELETE /api/review-logs/:id` - Delete review log (admin only)
- `PUT /api/review-logs/:id/score` - Manually override review score (admin only)

### Issue Trackers

- `GET /api/issue-trackers` - List issue tracker integrations (admin only)
- `POST /api/issue-trackers` - Create integration (admin only)
- `PUT /api/issue-trackers/:id` - Update integration (admin only)
- `DELETE /api/issue-trackers/:id` - Delete integration (admin only)
- `POST /api/issue-trackers/:id/test` - Test connection (admin only)

### Auto-Fix PR

- `POST /api/review-logs/:id/fix` - Request AI-generated fix PR/MR (admin only)
- `GET /api/review-logs/:id/fix-status` - Get fix status (admin only)

### Review Rules (CI/CD Policies)

- `GET /api/review-rules` - List review rules (admin only)
- `POST /api/review-rules` - Create rule (admin only)
- `PUT /api/review-rules/:id` - Update rule (admin only)
- `DELETE /api/review-rules/:id` - Delete rule (admin only)
- `POST /api/review-rules/evaluate/:id` - Test rules against a review log (admin only)

### Member Analysis

- `GET /api/members` - List member statistics
- `GET /api/members/detail` - Get member detail with trend and project stats
- `GET /api/members/overview` - Get team overview (total stats, trend, score distribution, top members)

### LLM Config

- `GET /api/llm-configs` - List LLM configs
- `GET /api/llm-configs/active` - List active LLM configs (for project selection)
- `POST /api/llm-configs` - Create LLM config
- `PUT /api/llm-configs/:id` - Update LLM config
- `DELETE /api/llm-configs/:id` - Delete LLM config

### Prompt Templates

- `GET /api/prompts` - List prompt templates
- `GET /api/prompts/:id` - Get prompt template detail
- `GET /api/prompts/default` - Get default prompt template
- `GET /api/prompts/active` - List active prompt templates
- `POST /api/prompts` - Create prompt template (admin only)
- `PUT /api/prompts/:id` - Update prompt template (admin only)
- `DELETE /api/prompts/:id` - Delete prompt template (admin only)
- `POST /api/prompts/:id/set-default` - Set as default template (admin only)

### IM Bots

- `GET /api/im-bots` - List IM bots
- `POST /api/im-bots` - Create IM bot
- `PUT /api/im-bots/:id` - Update IM bot
- `DELETE /api/im-bots/:id` - Delete IM bot

### Daily Reports

- `GET /api/daily-reports` - List daily reports
- `GET /api/daily-reports/:id` - Get daily report detail
- `POST /api/daily-reports/generate` - Generate daily report (manual, no notification)
- `POST /api/daily-reports/:id/resend` - Send/resend notification

### Webhooks

- `POST /webhook` - **Unified webhook (auto-detect GitLab/GitHub/Bitbucket, recommended)**
- `POST /review/webhook` - Alias for unified webhook
- `POST /api/webhook` - Unified webhook under /api prefix
- `POST /api/review/webhook` - Alias under /api prefix
- `POST /api/webhook/gitlab` - GitLab webhook (auto-detect project by URL)
- `POST /api/webhook/github` - GitHub webhook (auto-detect project by URL)
- `POST /api/webhook/gitlab/:project_id` - GitLab webhook (with project ID)
- `POST /api/webhook/github/:project_id` - GitHub webhook (with project ID)
- `POST /api/webhook/bitbucket` - Bitbucket webhook (auto-detect project by URL)
- `POST /api/webhook/bitbucket/:project_id` - Bitbucket webhook (with project ID)

### Sync Review (for Git Hooks)

- `POST /review/sync` - Synchronous code review for pre-receive hooks
- `POST /api/review/sync` - Same endpoint under /api prefix
- `GET /review/score?commit_sha=xxx` - Query review status/score by commit SHA
- `GET /api/review/score?commit_sha=xxx` - Same endpoint under /api prefix

Request body:

```json
{
  "project_url": "https://gitlab.example.com/group/project",
  "commit_sha": "abc123...",
  "ref": "refs/heads/main",
  "author": "John Doe",
  "message": "feat: add new feature",
  "diffs": "diff --git a/file.go..."
}
```

Response:

```json
{
  "passed": true,
  "score": 85,
  "min_score": 60,
  "message": "Score: 85/100 (min: 60)",
  "review_id": 123
}
```

See `scripts/pre-receive-hook.sh` for GitLab pre-receive hook example.

### System Logs

- `GET /api/system-logs` - List system logs
- `GET /api/system-logs/modules` - Get module list
- `GET /api/system-logs/retention` - Get log retention days
- `PUT /api/system-logs/retention` - Set log retention days
- `POST /api/system-logs/cleanup` - Manually cleanup old logs

### Health Check & Metrics

- `GET /health` - Service health check
- `GET /metrics` - Prometheus metrics

## Project Structure

```
codesentry/
├── backend/
│   ├── cmd/server/          # Application entry point
│   ├── internal/
│   │   ├── config/          # Configuration
│   │   ├── handlers/        # HTTP handlers
│   │   ├── middleware/      # Auth, CORS middleware
│   │   ├── models/          # Database models
│   │   ├── services/        # Business logic
│   │   └── utils/           # Utilities
│   └── go.mod
├── frontend/
│   ├── src/
│   │   ├── i18n/            # Internationalization
│   │   ├── layouts/         # Layout components
│   │   ├── pages/           # Page components
│   │   ├── services/        # API services
│   │   ├── stores/          # State management
│   │   └── types/           # TypeScript types
│   └── package.json
├── Dockerfile
├── docker-compose.yml
├── config.yaml.example
├── README.md
└── README_zh.md
```

## Tech Stack

### Backend

- Go 1.24
- Gin v1.11 (HTTP framework)
- GORM v1.31 (ORM)
- JWT authentication
- LDAP support

### Frontend

- React 19
- TypeScript 5.9
- Ant Design 5
- TanStack Query (data fetching & caching)
- Recharts
- Zustand (state management)
- React Router 7
- react-i18next (internationalization)
- react-markdown (review result rendering)

## License

MIT
