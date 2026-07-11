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
| `/uptime` | Uptime Kuma 回调监听:生成回调地址、把回调 JSON 解析成文案推给指定群,用法见 `/uptime help` | admin |
| `/cf` | Cloudflare 多凭据 + Worker 自定义域名管理 + 域名记录库(大类/小类/状态),用法见 `/cf help` | admin |
| `/kuma` | Uptime Kuma REST 包装器管理:绑定地址/Key,给指定域名或整个大类批量建监控,用法见 `/kuma help` | admin |
| `/panel` | 生成后台面板的一次性登录码:`/panel new|list|revoke` | admin |
| `/help` | 自动生成的命令列表 | 公开 |

## 细节

**接口巡检(`/pg`)** — 每个分组保存 URL、请求方式、header、请求体和返回模板。模板可以取状态码、按路径取 JSON 字段、读响应头,也可以把响应当图片发出来;`{body_file}` 把响应体存成文件发送(HTML 响应存成 `.html`,点开进浏览器),`{body_file["报告.html"]}` 可以改名,不写名字按 Content-Type 自动起。`{{变量}}` 引用统一管理的变量表,找不到时回退环境变量;`/pg run <组> 键=值` 临时传参,解析不到的 `{{变量}}` 会提示"需要手动填写参数"并给出用法,不会带着原文乱发请求;`/pg sched <组> <cron> [键=值]` 配定时巡检,加上断言就能在失败时告警——连续失败达到阈值才报,恢复后会补一条通知。

**会话打包(`/bundle`)** — `start` 到 `end` 之间的消息会被打包成一个随机链接,无法被猜测或枚举。浏览器打开是白底圆角的聊天页面;curl 或加 `?format=json` 拿到结构化 JSON,可以直接交给 AI 处理。图片、sticker 和 20MB 以内的视频会下载并内嵌展示,文件类消息不收。

**Uptime Kuma 回调(`/uptime`)** — 与 `/pg` 的主动巡检相反,这是被动接收:`/uptime new <名字>` 建一个监听,返回一条不可枚举的回调地址(`<PUBLIC_BASE_URL>/hook/uptime/<token>`,和 bundle 共用同一个 HTTP 服务),把它填到 Uptime Kuma 的「Webhook」通知里即可;`/uptime url <名字>` 单独再打印一次地址。`/uptime target <名字> here` 把通知目标绑到当前会话(群里就发群 id),回调到达时按配置解析后推过去。文案两种写法:`/uptime fields <名字> ipgroup,isbanned` 把回调 JSON 里的这些字段(支持 `monitor.name` 点路径)渲染成「字段: 值」逐行输出;`/uptime template <名字> <文案>` 用 `{a.b}` 占位自定义整段文案。带 `heartbeat.status` 的载荷(Kuma 默认格式)会自动在标题标出上线/掉线。`/uptime test <名字> [JSON]` 用示例或自带 JSON 演练一次。

**Cloudflare 域名管理(`/cf`)** — 面向多账号,按「有的域名」操作而不锁定某个 zone。`/cf cred add <名> <account_id> <api_token>` 存多个凭据(用 scoped API Token,需 Workers Scripts 编辑 + Zone/DNS 权限;token 显示时打码)。`/cf worker add <worker> <凭据> [大类]` 把 Worker 登记到某个凭据下,并可绑一个「大类」;`/cf worker domains <worker>` 直接从 Cloudflare 查这个 worker 现有绑定的域名(实时,不是本地记录,并标注每个域名在记录库里的状态或「未入库」)。域名记录库分三层:大类(哪个项目)、小类(可选子分组)、状态(未使用/已使用/被ban);`/cf domain add <大类> <域名[,域名...]> [小类]` 批量录入。录入后可以随时改分类:`/cf domain set <域名> category <大类>` / `sub <小类>`。每条域名记录还能记生命周期字段:`/cf domain set <域名> <字段> <值>`,字段有 `purchased`(购买日期)、`usage`(准备用在哪里)、`dns`(DNS 是什么)、`ready`(是否就绪 yes/no)、`changed`(更换时间);`/cf show <域名>` 查看全部字段,`/cf domain list` 会给就绪的域名标 ✅。绑定成功时会自动记一次「更换时间」。核心操作 `/cf bind <worker> [域名] [force]` 走 Worker Custom Domains:不给域名时自动从该 worker 大类里取一个「未使用」的域名,自动从域名解析出 zone 并绑定,成功后把域名标记「已使用」;如果域名已绑到别的 worker 会先提示,加 `force` 才强制替换。`/cf unbind <域名>` 解绑并回到「未使用」,`/cf show <worker|域名>` 看当前记录。凭据/Worker/域名都持久化在 `cloudflare.json`(0600)。

**Uptime Kuma 建监控(`/kuma`)** — 与 `/uptime`(接收 Kuma 回调)相反,这是主动调 Kuma 建监控。vanilla Kuma 没有建监控的 REST 接口,所以对接 [keithah/uptime-kuma-rest-api](https://github.com/keithah/uptime-kuma-rest-api) 这类包装器:`/kuma cred add <名> <base_url> [api_key]` 绑定包装器地址(API Key 仅在你给包装器加了鉴权时需要,以 Bearer 发送,不加就留空;显示打码)。`/kuma add <名> <域名>` 给指定域名建一个 `https://<域名>` 的 HTTP 监控;`/kuma add <名> all [大类]` 则把 `/cf` 记录库里的域名批量建监控(全局或按大类,自动跳过「被ban」)。`/kuma list <名>` 看现有监控,`/kuma del <名> <monitor_id>` 删。凭据存在 `kuma.json`(0600)。

**后台面板(`panel`)** — 一个干净的网页后台,和 bundle 共用同一个 HTTP 服务,地址 `<PUBLIC_BASE_URL>/panel/`。**一次性授权码登录**:admin 在 bot 里 `/panel new` 生成一个一次性登录码,发给需要的人;在登录页输入后签发 HMAC 签名的 HttpOnly cookie,**有效期 30 天**(反代跑 https 时自动带 Secure)。码用一次即失效,`/panel list` 看待用码,`/panel revoke <码>` 撤销。面板覆盖前面几个功能:Cloudflare 域名记录(录入、改分类/状态/生命周期字段、绑定/解绑 Worker Custom Domain)、Uptime Kuma 监控(建/删/查)、以及只读查看 Worker、凭据、回调监听。带密钥的凭据/Worker 创建仍留在 bot(`/cf cred`、`/kuma cred`)。界面是密集型仪表盘风格,自适应桌面/移动,支持浅色/深色。签名密钥与待用码存在 `panel.json`(0600)。想彻底关掉面板就把 `panel` 插件加进黑名单。

**在群里用** — 群里发 `/whoami` 拿到群的 chat id(负数),`/promote` 这个 id 之后,巡检告警就会推到群里。如果还想收集群里的普通消息(会话打包),需要在 @BotFather 里关掉 bot 的隐私模式。

## 配置

| 环境变量 | 默认值 | 说明 |
|---|---|---|
| `BOT_TOKEN` | (必填) | bot token |
| `BOT_NAME` / `BOT_DESCRIPTION` / `BOT_ABOUT` | (空=不动) | 启动时同步 bot 显示名 / 描述 / 简介,等价 BotFather 的 /setname、/setdescription、/setabouttext |
| `BUNDLE_ADDR` | `:8099` | 共享 HTTP 服务监听地址(bundle 与插件回调路由都挂在这里) |
| `BUNDLE_BASE_URL` | `http://localhost:8099` | 生成链接时的前缀 |
| `BUNDLE_MEDIA_DIR` | `bundle-media` | 媒体文件目录 |
| `PUBLIC_BASE_URL` | (空=同 `BUNDLE_BASE_URL`) | 插件 HTTP 路由(如 `/uptime` 回调)对外前缀,和 bundle 同一个服务;一般不用单独设 |
| `PLUGINS_MODE` / `PLUGINS_LIST` | `blacklist` / 空 | 插件开关:blacklist=列表里的禁用,whitelist=只启用列表里的;逗号分隔,如 `PLUGINS_LIST=echo,bundle` |
| `AUTH_STORE` / `PLAYGROUND_STORE` / `VARS_STORE` / `BUNDLE_STORE` / `UPTIME_STORE` / `CLOUDFLARE_STORE` / `KUMA_STORE` / `PANEL_STORE` | `*.json` | 各持久化文件路径 |

监听地址和链接前缀是分开的:套反向代理时,可以只监听 `127.0.0.1:8099`,把 `BUNDLE_BASE_URL` 设成对外的 https 域名。

## 写一个插件

仓库内插件放在 `internal/plugins/`，实现 `plugin.Plugin` 并在 `init()` 中注册：

```go
package hello

import "github.com/moyoez/magibox/pkg/plugin"

type Plugin struct{ plugin.Base }

func (Plugin) Name() string { return "hello" }

func init() { plugin.Register(Plugin{}) }
```

仓库外插件使用独立 Go Module，导入 `pkg/plugin`；需要管理员权限时使用
`pkg/auth.RequireAdmin()`。独立宿主先空导入自己的插件集合，再调用共享启动入口：

```go
package main

import (
    "log"

    "github.com/moyoez/magibox/pkg/magibox"
    _ "example.com/my-host/plugins"
)

func main() {
    if err := magibox.Run(); err != nil {
        log.Fatal(err)
    }
}
```

依赖方向始终是外部宿主依赖 MagiBox；MagiBox 本身不依赖外部插件仓库。
