# CodeSentry

<div align="center">
  <img src="https://raw.githubusercontent.com/huangang/codesentry/main/frontend/public/codesentry-icon.png" alt="CodeSentry Logo" width="120" height="120">
</div>

> **声明 / Disclaimer**:
> 本项目为基于 [huangang/codesentry](https://github.com/huangang/codesentry) 二次开发的分支版本，主要用于**学习、教育及架构研究用途**。
> 感谢原作者 [huangang](https://github.com/huangang) 提供的优秀开源基础。

CodeSentry 是一款具备双引擎 (V1/V2) 智能上下文解析与超大 PR 自动分批审查能力的专业级 AI 代码审查系统，支持 GitHub、GitLab。

## 技术栈

- **后端**: Go 1.24+ (Fiber, GORM, Tree-sitter AST 解析)
- **前端**: React 18, TypeScript, Vite, TailwindCSS
- **数据库**: PostgreSQL
- **队列/缓存**: Redis (用于异步任务和去重)
- **大模型接入**: 原生支持 OpenAI, Anthropic (Claude), Ollama, Google Gemini

## 核心架构与操作流程

以下时序图展示了从代码提交到 AI 返回审查结果的完整交互过程。
*(阅图指南：从上往下看，箭头代表动作的发起方和接收方，虚线代表数据的返回。)*

```mermaid
sequenceDiagram
    autonumber
    participant Git as GitLab / GitHub
    participant Webhook as CodeSentry API
    participant Parser as 解析流水线 (Parser)
    participant LLM as 大语言模型 (AI)

    Git->>Webhook: Webhook 监听 Push/MR，传递信息给CodeSentry API
    
    rect rgb(240, 248, 255)
        Note over Webhook, Git: 第一阶段：获取 Diff 并筛选核心文件
        Webhook->>Git: 请求获取本次 Commit 的 Diff 补丁
        Git-->>Webhook: 返回 Diff 数据 (只包含 + 和 - 的代码行)
        Webhook->>Parser: 过滤掉 lock 文件、静态资源等
        Parser->>Parser: 按照代码修改量降序排列，只保留前 N 个核心文件
    end

    rect rgb(245, 245, 255)
        Note over Webhook, Git: 第二阶段：获取完整源码并解析 (V1/V2)
        Webhook->>Git: 请求获取这 N 个文件的【完整源代码】 (用于构建 File Context)
        Git-->>Webhook: 返回被修改文件的完整源码
        
        alt V1 模式 (轻量级)
            Parser->>Parser: 在完整源码中，截取 Diff 上下 10 行并合并
        else V2 模式 (专家级)
            Parser->>Parser: 组装 File Context (AST 提取完整函数块)
            Webhook->>Git: 请求获取整个仓库的【全量文件树及源码】 (受限于接口能力及配置)
            Git-->>Webhook: 返回全量源码 (或 fallback 到仅 Diff 文件)
            Parser->>Parser: 组装 Callers Context (全局扫描谁调用了被改函数)
            Parser->>Parser: 组装 Callee Context (全局扫描被改代码调用的底层规范)
        end
    end

    Parser->>Parser: 将提取到的上下文注入到 Prompt 模板中

    rect rgb(245, 255, 250)
        Note over Parser, LLM: 第三阶段：智能分批与 AI 审查
        alt 提示词 Token 未超标
            Parser->>LLM: 单次发送全量 Prompt
            LLM-->>Parser: 返回审查报告
        else 提示词 Token 超标 (Chunking)
            Parser->>Parser: 将文件拆分成多个 Batch (例如 Batch 1 包含前 3 个文件)
            par 并发请求
                Parser->>LLM: 发送 Batch 1
                Parser->>LLM: 发送 Batch 2
            end
            LLM-->>Parser: 返回各自的审查报告
            Parser->>Parser: 聚合多个报告，加权计算最终总分
        end
    end

    Webhook->>Git: 将最终的审查报告写回 PR 评论区 / Commit 留言
```

## 提示词工程 (Prompt Engineering) 参考示例

为了让大模型能够精准地理解代码并输出结构化的审查报告，系统采用了**变量注入**和**严格指令**相结合的 Prompt 设计。以下是系统推荐的标准提示词模板，它完美契合了 V2 引擎的双向追溯和孤儿代码提取逻辑：

```markdown
你是一位资深的软件开发工程师。你的任务是对提交的代码进行专业、聚焦的代码审查。 
 
## 核心审查原则（最高优先级，绝对服从） 
1. **绝对聚焦修改行**：你**只能**审查带有 `+`（新增）和 `-`（删除）标记的代码行，以及它们对当前函数逻辑的直接影响。 
2. **无视历史遗留问题**：严禁指出未修改代码（没有 `+` 或 `-` 标记的行）中的 Bug、不规范或优化空间。只要不是这次改动引入的，就当没看见。 
3. **严格的跨文件双向校验**： 
   - **向外看 (Callers)**：如果本次修改改变了某函数的输入/输出定义，必须检查下方的【跨文件调用影响分析】，确认是否导致其他调用方崩溃。 
   - **向内看 (Callee)**：如果本次修改新增或修改了对某个底层函数的调用，必须检查下方的【被调用函数定义参考】，确认传入的参数类型和数量是否符合原函数要求。 
 
## 评分维度（总分 100 分） 
1. **修改逻辑的正确性（50 分）**：本次修改是否达到了预期目的，有没有引入新的 Bug。 
2. **跨模块兼容性（30 分）**：本次修改是否破坏了上下游的调用约定（重点结合 Callers/Callee 检查）。 
3. **代码健壮性与安全（15 分）**：新增的代码是否有安全隐患或可能导致异常。 
4. **提交信息质量（5 分）**：commit 信息是否清晰。 
 
## 输出格式（Markdown） 
请严格按照以下结构输出，不要说多余的废话： 
 
### 一、修改点审查意见（按函数分组） 
（请遍历提供的 Context，以被修改的函数/类为单位进行输出。如果该函数修改没有问题，请写“✅ 该函数修改无异常”） 
 
- **函数/模块名**：`generate_report` (文件: `Performance_monitor.py`) 
  - **问题/风险**：[直接说明带 `+` 或 `-` 的行有什么问题，或者与 Callers/Callee 有什么冲突] 
  - **优化建议**：[给出具体的修改代码] 
 
### 二、评分明细 
- 按四个评分维度给出具体分数和简短理由。 
 
### 三、总分（特别重要） 
- 格式必须为："总分:XX 分"（例如：总分:80 分）。 
 
--- 
**本次修改的代码及完整上下文参考 (Context & Diff)**： 
（带 `+` 和 `-` 标记的行代表本次修改，你只需关注这些行及其直接影响） 
{{file_context}} 
 
--- 
**跨文件调用影响分析 (Callers Context)**： 
（调用了本次修改函数的上游代码。请检查本次修改是否导致它们报错） 
{{callers_context}} 
 
--- 
**被调用函数定义参考 (Callee Context)**： 
（本次新增/修改代码中调用的底层函数定义。请检查本次调用传参是否正确） 
{{callee_context}} 
 
--- 
**提交历史（commits）**： 
{{commits}}
```

## 双引擎解析逻辑 (V1 vs V2)

为了在“审查精度”与“Token 成本/响应速度”之间取得最佳平衡，系统内置了两套代码解析引擎：

### V1 模式：轻量级极速审查

- **定位**：适合日常小迭代、前端 UI 微调、配置文件修改，成本极低。
- **逻辑**：不解析语法，纯文本提取修改点（Diff）上下 10 行。若多个修改点距离较近，自动合并为一个连贯代码块。

**V1 处理流程示例**：

```text
修改点 1 (Line 100) -> 提取 90~110 行
修改点 2 (Line 115) -> 提取 105~125 行
--- 智能合并 ---
最终发给 AI：提取 90~125 行 (包含两个修改点，无割裂感)
```

### V2 模式：专家级深度审查 (AST 语法树)

- **定位**：适合核心业务重构、底层数据结构变更，主打高精度防雷。
- **逻辑**：
  1. **Function Context**：将修改点所在的**整个函数/类**完整提取出来。
  2. **Callers Context (向上追溯)**：全网扫描谁调用了被修改的函数。
  3. **Callee Context (向下校验)**：全网扫描本次修改中调用的底层函数定义。
  4. **Orphan Hunks (孤儿代码)**：自动捕获全局变量、import 导入等不在函数内的代码。

**V2 处理流程示例**：

```text
用户修改了 `user.go` 中的 `func CheckAuth(token string) bool` -> 改为了 `func CheckAuth(token string, age int) bool`

系统自动收集并发给 AI：
1. [File Context]: 完整的 `CheckAuth` 源码，并用 + 和 - 标出改动。
2. [Callers Context]: 自动从 `api.go` 提取调用了 `CheckAuth` 的代码片段（AI 借此发现 api.go 还在传 1 个参数，抛出致命 Bug）。
```

## 核心 API 接口说明

### 后端核心接口

- `POST /api/webhook/:platform/:uuid`
  - **功能**: 接收代码托管平台的 Webhook 事件，触发审查队列。
- `GET /api/projects`
  - **功能**: 获取项目列表，配置代码仓库的鉴权信息、使用的 LLM 模型及审查模式 (V1/V2)。
- `POST /api/projects`
  - **功能**: 新增项目绑定。
- `PUT /api/prompts/:id`
  - **功能**: 更新提示词模板，支持动态注入 `{{file_context}}`、`{{callers_context}}` 等上下文变量。
- `GET /api/logs/review`
  - **功能**: 查询历史审查日志，支持分页、状态检索。
- `POST /api/logs/review/batch-retry`
  - **功能**: 批量重新触发失败或不满意的审查任务。
- `GET /metrics`
  - **功能**: Prometheus 监控指标接口，实时暴露队列堆积、API 耗时及大模型请求状态。

### 前端核心接口服务 (`src/services/api.ts`)

- `api.getProjects()` / `api.createProject()`: 项目管理接口调用。
- `api.getReviewLogs()`: 获取审查流水和统计数据。
- `api.getPrompts()` / `api.updatePrompt()`: 系统 Prompt 管理。

## 编译与 Docker 打包部署流程

### 1. 本地编译与运行 (开发环境)

**后端编译**:

```bash
cd backend
# 复制并修改配置文件 (配置 Postgres 连接)
cp ../config.yaml.example config.yaml
# 运行后端服务
go run ./cmd/server
```

**前端编译**:

```bash
cd frontend
npm install
npm run dev
```

### 2. 内网离线一键部署 (基于 docker-compose)

生产环境推荐使用项目根目录下的 `docker-compose.yml` 进行一键编排，它会自动拉起 **CodeSentry 主程序** 和 **PostgreSQL 数据库**。

**第一步：加载离线镜像**
在内网服务器上，将准备好的两个离线镜像包加载进 Docker：

```bash
# 1. 加载 PostgreSQL 数据库镜像
docker load -i postgres.tar

# 2. 加载 CodeSentry 主程序镜像
docker load -i code-reviewer-aoi.tar
```

**第二步：一键启动**
确保当前目录下有 `docker-compose.yml` 文件，直接执行：

```bash
docker-compose up -d
```

**`docker-compose.yml`** **核心配置说明**:

```yaml
version: '3.8'

services:
  # 1. db节点 (PostgreSQL): 使用加载好的 postgres 镜像。
  # 数据卷挂载在 ./data/postgres，保证容器重启或更新时数据不丢失。
  db:
    image: postgres:15-alpine
    container_name: code_review_db
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: "123456"
      POSTGRES_DB: codesentry
    volumes:
      - ./data/postgres:/var/lib/postgresql/data
    ports:
      - "5432:5432"

  # 2. app节点 (CodeSentry): 依赖 db 启动。
  # 通过环境变量（如 DB_DSN）直接注入数据库连接信息，通过 OPENAI_API_KEY 注入大模型密钥。
  # 配置文件和日志挂载在 ./data/app。
  # 端口访问：本机的 8080 端口映射到了主程序的 Web 界面和 API 端口，启动后直接访问 http://IP:8080 即可。
  app:
    image: zhazha/code-reviewer-aoi:latest
    container_name: codesentry_app
    depends_on:
      - db
    ports:
      - "8080:8080"
    environment:
      # 对应 config.yaml 中的 server.port 和 server.mode
      - SERVER_PORT=8080
      - SERVER_MODE=release
      # 对应 config.yaml 中的 database.driver 和 database.dsn
      - DB_DRIVER=postgres
      - DB_DSN=host=db user=postgres password=123456 dbname=codesentry port=5432 sslmode=disable TimeZone=Asia/Shanghai
      # 对应 config.yaml 中的 jwt.secret 和 jwt.expire_hour
      - JWT_SECRET=your-secret-key-change-in-production
      - JWT_EXPIRE_HOUR=24
      # 大模型配置
      - OPENAI_API_KEY=your_openai_api_key
      - OPENAI_BASE_URL=https://api.openai.com/v1
    volumes:
      - ./data/app:/app/data
```

## 目录结构

```text
codesentry/
├── backend/                  # Go 后端服务
│   ├── cmd/
│   │   ├── scripts/          # 维护脚本 (更新分数、规则等)
│   │   └── server/           # 主程序启动入口 (main.go)
│   ├── internal/             # 核心私有逻辑
│   │   ├── config/           # yaml 配置文件解析
│   │   ├── handlers/         # HTTP API 路由与控制器层
│   │   ├── middleware/       # 中间件 (Auth鉴权, CORS, RateLimit等)
│   │   ├── models/           # GORM 数据库实体模型定义
│   │   ├── services/         # 核心业务逻辑层
│   │   │   ├── webhook/      # Webhook 事件接收与解析引擎
│   │   │   ├── ai.go         # 大模型调用与 Chunking 拆分引擎
│   │   │   ├── file_context.go # V1 上下文提取与合并引擎
│   │   │   ├── repo_map.go   # V2 AST 语法树与 Callers/Callee 追溯引擎
│   │   │   └── task_queue.go # 异步任务队列与 Redis 调度
│   │   └── utils/            # JWT、密码加密等通用工具类
│   ├── pkg/                  # 公共包 (Logger, 统一响应封装等)
│   ├── go.mod                # Go 依赖管理
│   └── .air.toml             # Air 热重载配置
├── frontend/                 # React 18 前端界面
│   ├── public/               # 静态资源 (图标, 图片等)
│   ├── src/
│   │   ├── components/       # 全局复用组件 (通知, 搜索, 图表等)
│   │   ├── constants/        # 全局常量与权限配置
│   │   ├── hooks/            # 自定义 Hooks (React Query 数据获取等)
│   │   ├── i18n/             # 国际化多语言配置 (中/英)
│   │   ├── layouts/          # 整体页面骨架布局
│   │   ├── pages/            # 核心业务页面视图
│   │   ├── services/         # Axios API 请求封装
│   │   ├── stores/           # Zustand 全局状态管理 (Auth, Theme)
│   │   └── types/            # TypeScript 全局接口定义
│   ├── package.json          # Node 依赖管理
│   └── vite.config.ts        # Vite 构建配置
├── Dockerfile                # 生产环境容器化打包脚本
├── docker-compose.yml        # 容器编排部署文件
├── config.yaml.example       # 后端配置示例文件
└── README.md                 # 项目说明文档
```

## 许可证 (License)

MIT License
