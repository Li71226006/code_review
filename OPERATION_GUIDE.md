# CodeSentry 操作与使用指南

本指南将帮助您快速了解如何在部署成功后，通过 Web 后台配置并使用 CodeSentry 代码审查系统。

## 1. 登录与初始化

1. **访问系统后台**：在浏览器中打开 `http://<您的服务器IP>:8080`。
2. **默认账号**：首次部署后，使用默认超级管理员账号登录：
   - 用户名：`admin`
   - 密码：`admin`
3. *(强烈建议)* 登录后，立即点击右上角个人中心，**修改默认密码**以保障系统安全。

## 2. 配置大语言模型 (LLM)

要让 AI 开始工作，您必须先配置至少一个大模型。

1. 在左侧菜单栏点击 **“大模型配置 (LLM Config)”**。
2. 点击 **“新建模型”**：
   - **名称**：例如 `OpenAI GPT-4o` 或 `公司内网千问`。
   - **平台**：选择对应的模型供应商（如 OpenAI、Anthropic、Ollama 或自建代理）。
   - **API Key**：填入您的密钥。
   - **Base URL**：如果使用代理地址或私有部署，请填入对应的 URL（如 `https://api.openai.com/v1`）。
3. 保存并点击 **“测试连接”**，确保系统能够正常连通大模型。

## 3. 提示词 (Prompt) 模板管理

系统内置了标准提示词模板，您也可以根据团队规范进行自定义。

1. 在左侧菜单栏点击 **“提示词管理 (Prompts)”**。
2. 您可以新建一个模板，并**必须**在其中包含以下系统内置变量占位符：
   - `{{file_context}}`：被修改文件的上下文。
   - `{{callers_context}}`：调用方上下文（V2模式专用）。
   - `{{callee_context}}`：底层定义上下文（V2模式专用）。
3. 详细的提示词编写技巧，请参考主目录下的 `README.md` 中的【提示词工程参考示例】。

## 4. 接入代码仓库 (配置访问令牌)

为了让系统能够拉取您的代码并回写审查评论，您必须在代码托管平台上生成**访问令牌 (Access Token)** 并配置到 CodeSentry 的“项目管理”中。

### 4.1 GitLab 令牌配置指南
**如何生成**：
1. 登录 GitLab，进入您要审查的**项目 (Project)** 页面。
2. 在左侧菜单栏选择 **Settings (设置)** -> **Access Tokens (访问令牌)**。
3. 点击 `Add new token` (新建令牌)，填写名称和过期时间。
4. **必开权限 (Scopes)**：
   - `api`：(必须) 允许系统调用 API 获取完整文件、拉取分支树并回写评论。
   - `read_repository`：(必须) 允许系统读取您的代码。
5. 点击创建后，**立刻复制生成的 Token 字符串**。

### 4.2 GitHub 令牌配置指南
**如何生成**：
1. 登录 GitHub，点击右上角头像进入 **Settings (设置)**。
2. 滚动到底部左侧点击 **Developer settings (开发者设置)** -> **Personal access tokens** -> **Tokens (classic)**。
3. 点击 `Generate new token (classic)`。
4. **必开权限 (Scopes)**：
   - `repo` (勾选整个 repo 节点)：(必须) 允许系统拉取私有仓库代码、获取 Diff 并发表评论。
5. 点击创建后，**立刻复制生成的 Token 字符串**。

### 4.3 在 CodeSentry 中绑定
拿到令牌后，回到 CodeSentry 系统：
1. 左侧菜单栏点击 **“项目管理 (Projects)”** -> **“新建项目”**。
2. **仓库地址**：填写您项目的 URL（如 `https://gitlab.company.com/backend/user-service`）。
3. **鉴权 Token**：填入您刚才复制的访问令牌。
4. **审查引擎模式**：选择 V1 (轻量级) 或 V2 (专家深度级)。
5. **保存**，此时系统已具备拉取该项目代码的权限。

## 5. 在 Git 平台上配置 Webhook

配置好令牌后，还需要配置 Webhook。它的作用是：**当有人提交代码时，Git 平台能主动通知 CodeSentry 去执行审查**。

**注意 Webhook URL 的格式**：
Webhook 的地址必须设置为：**`http://软件部署的电脑的IP:端口号/webhook`**（例如：`http://192.168.1.100:8080/webhook`）。

### 5.1 GitLab Webhook 设置
1. 进入该项目的 **Settings (设置)** -> **Webhooks**。
2. **URL**：填写 `http://您的IP:8080/webhook`。
3. **Secret Token**：如果 CodeSentry 后台的“系统设置”里配置了 Webhook Secret，请填入这里；如果没有则留空。
4. **触发事件 (Triggers) - 必开权限**：
   - 勾选 **Push events** (代码推送到分支时触发)
   - 勾选 **Merge request events** (创建或更新合并请求时触发)
5. 取消勾选 SSL verification（如果是内网 http 部署）。
6. 点击 `Add webhook` 并测试发送一个 Push 事件。

### 5.2 GitHub Webhook 设置
1. 进入该仓库的 **Settings (设置)** -> 左侧的 **Webhooks**。
2. 点击 **Add webhook**。
3. **Payload URL**：填写 `http://您的IP:8080/webhook`。
4. **Content type**：必须选择 **`application/json`**。
5. **Secret**：填入 CodeSentry 的 Webhook Secret（如果有）。
6. **触发事件 (Which events would you like to trigger this webhook?) - 必开权限**：
   - 选择 `Let me select individual events.`
   - 勾选 **Pushes**
   - 勾选 **Pull requests**
7. 确保勾选了 `Active`，然后点击 `Add webhook` 保存。

## 6. 查看审查日志与结果

完成上述配置后，当开发人员推送代码或提交 MR 时：

1. **开发侧体验**：
   - AI 会在 10-30 秒内完成代码阅读。
   - 发现问题后，AI 会自动在对应的 GitLab/GitHub Commit 页面或 MR 讨论区里发表详细的 Markdown 评论和打分。
2. **管理侧体验**：
   - 在 CodeSentry 后台的 **“审查日志 (Review Logs)”** 页面，您可以实时看到所有的审查任务状态（排队中、审查中、成功、失败）。
   - 点击某条日志的详情，可以完整看到系统发送给大模型的 Prompt 原始内容，以及大模型返回的原始 JSON，方便排查报错和优化提示词。

## 7. 高级功能配置 (可选)

- **IM 机器人通知**：在“IM Bots”菜单中配置钉钉/飞书机器人的 Webhook，当发生低分提交或严重漏洞时，自动推送到群聊。
- **拦截不合格代码**：参考同步 API (Sync API) 接口说明，在 Git 服务器端配置 `pre-receive` hook，可以直接拒绝得分低于 60 分的代码 Push。
