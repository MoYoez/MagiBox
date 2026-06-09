# 📦 Magibox

Playground, Plugin Like, Simple Telegram Bot.

## 快速开始

```powershell
$env:BOT_TOKEN = "<token>" 
go run ./cmd/bot
```

第一次启动时,终端会打印一行 `/bind <配对码>`。把它发给 bot,就完成了 owner 绑定。

## Docker

```bash
cp .env.example .env   # 填入 BOT_TOKEN 等
docker compose up -d --build
```

不用 compose 的话:

```bash
docker build -t magibox .
docker run -d --name magibox \
  -e BOT_TOKEN=<token> \
  -e BUNDLE_BASE_URL=https://example.com \
  -p 8099:8099 -v magibox-data:/data magibox
```

## 命令

| 命令 | 说明 | 权限 |
|---|---|---|
| `/ping` | 存活检测 | 公开 |
| `/whoami` | 查看 chat id / user id 和角色;在群里会带上群 id;回复某条消息可以看对方的 id | 公开 |
| `/bind <码>` | 用终端打印的配对码绑定 owner | 公开 |
| `/members` | 列出所有 admin / owner | admin |
| `/promote` `/demote <chat_id>` | 升降角色,也可以直接回复目标消息;promote 一个群 id 后,推送会发到那个群 | owner |
| `/pg` | HTTP 接口 playground,用法见 `/pg help` | admin |
| `/pg_run` `/pg_sched` `/pg_new` … | `/pg` 子命令的独立版本,参数相同,输 `/` 有补全 | admin |
| `/<组名> [键=值 …]` | playground 分组直达命令,等价 `/pg run <组名>`;新建分组自动注册,删除自动移除(组名需为 a-z0-9_) | admin |
| `/bundle start/end/status/cancel` | 会话打包 | 公开 |
| `/help` | 自动生成的命令列表 | 公开 |

## 细节

**接口巡检(`/pg`)** — 每个分组保存 URL、请求方式、header、请求体和返回模板。模板可以取状态码、按路径取 JSON 字段、读响应头,也可以把响应当图片发出来。`{{变量}}` 引用统一管理的变量表,找不到时回退环境变量;`/pg run <组> 键=值` 临时传参;`/pg sched <组> <cron> [键=值]` 配定时巡检,加上断言就能在失败时告警——连续失败达到阈值才报,恢复后会补一条通知。

**会话打包(`/bundle`)** — `start` 到 `end` 之间的消息会被打包成一个随机链接,无法被猜测或枚举。浏览器打开是白底圆角的聊天页面;curl 或加 `?format=json` 拿到结构化 JSON,可以直接交给 AI 处理。图片、sticker 和 20MB 以内的视频会下载并内嵌展示,文件类消息不收。

**在群里用** — 群里发 `/whoami` 拿到群的 chat id(负数),`/promote` 这个 id 之后,巡检告警就会推到群里。如果还想收集群里的普通消息(会话打包),需要在 @BotFather 里关掉 bot 的隐私模式。

## 配置

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `BOT_TOKEN` | (必填) | bot token |
| `BOT_NAME` / `BOT_DESCRIPTION` / `BOT_ABOUT` | (空=不动) | 启动时同步 bot 显示名 / 描述 / 简介,等价 BotFather 的 /setname、/setdescription、/setabouttext |
| `BUNDLE_ADDR` | `:8099` | bundle 服务监听地址 |
| `BUNDLE_BASE_URL` | `http://localhost:8099` | 生成链接时的前缀 |
| `BUNDLE_MEDIA_DIR` | `bundle-media` | 媒体文件目录 |
| `PLUGINS_MODE` / `PLUGINS_LIST` | `blacklist` / 空 | 插件开关:blacklist=列表里的禁用,whitelist=只启用列表里的;逗号分隔,如 `PLUGINS_LIST=echo,bundle` |
| `AUTH_STORE` / `PLAYGROUND_STORE` / `VARS_STORE` / `BUNDLE_STORE` | `*.json` | 各持久化文件路径 |

监听地址和链接前缀是分开的:套反向代理时,可以只监听 `127.0.0.1:8099`,把 `BUNDLE_BASE_URL` 设成对外的 https 域名。

## 写一个插件

在 `internal/plugins/` 下新建一个包,实现 `Name()` 和需要的扩展点(`Commands()` 命令、`Jobs()` 定时任务、`Wire()` 消息流),在 `init()` 里调用 `plugin.Register`,最后在 `internal/plugins/all.go` 里加一行空导入。主程序和其他插件不用动。
