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

## 4. 接入代码仓库 (Projects)

1. 在左侧菜单栏点击 **“项目管理 (Projects)”** -> **“新建项目”**。
2. 填写仓库信息：
   - **项目名称**：例如 `User-Service-Backend`。
   - **代码平台**：选择 GitLab 或 GitHub。
   - **仓库地址**：例如 `https://gitlab.company.com/backend/user-service`。
   - **鉴权 Token**：填入具有该仓库读取权限的 Personal Access Token。
3. **选择审查引擎模式**：
   - **V1 (轻量级)**：速度快，适合日常小修改。
   - **V2 (专家级)**：速度稍慢但精度极高，会进行跨文件 AST 追溯，适合核心代码库。
4. **选择默认大模型**：为该项目指定刚刚配置好的 LLM。

## 5. 在 Git 平台上配置 Webhook

系统创建好项目后，需要在您的 GitLab/GitHub 仓库里配置 Webhook，让代码提交事件能推送到 CodeSentry。

1. 进入 GitLab/GitHub 的仓库设置页面 -> **Webhooks**。
2. **Payload URL**：填写 CodeSentry 提供的统一 Webhook 接收地址：
   - `http://<您的服务器IP>:8080/api/webhook/auto/default`
3. **Secret Token**：在 CodeSentry “系统设置”中可以查看到全局 Webhook Secret，填入此处以保证安全。
4. **触发事件 (Triggers)**：
   - 勾选 **Push events** (代码推送)
   - 勾选 **Merge request events / Pull requests** (合并请求)
5. 保存并点击 “Test” 发送测试请求，如果返回 200 OK 即代表接入成功。

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
