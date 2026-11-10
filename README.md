# 12306 余票监控提醒（Go，个人自用版）

只监控，不自动下单。发现指定车次/席别有票就在终端响铃提醒，由你自己去 12306 下单。

**前置要求：系统里要有 curl。** Windows 10/11 自带 `curl.exe`，先在命令行跑 `curl --version` 确认一下。原因是 12306 会按 TLS 指纹识别客户端，Go 标准库的 TLS 指纹会被直接挡回一个 HTML 验证页，只有 curl 这种浏览器级指纹能正常拿到 JSON，所以程序是调系统 curl 来查票的。

## 编译成 exe（Windows）

```bash
# 在本机（Windows）直接编
go build -o ticket-monitor.exe .

# 或在 Mac/Linux 上交叉编译给 Windows 用
GOOS=windows GOARCH=amd64 go build -o ticket-monitor.exe .
```

## 运行（以明天镇江 → 北京为例）

```bash
# 只监控 G550、G600、G738 的二等座，每 30 秒查一次
ticket-monitor.exe -date 2026-11-11 -from ZEH -to VNP -trains G550,G600,G738 -seat 二等座

# 只查一次，用来测试
ticket-monitor.exe -once

# 监控全部车次的一等座
ticket-monitor.exe -date 2026-11-11 -from ZEH -to VNP -seat 一等座
```

## 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `-date` | 2026-11-11 | 出发日期 YYYY-MM-DD |
| `-from` | ZEH | 出发站：镇江南 ZEH / 镇江 ZJH |
| `-to` | VNP | 到达站：北京南 VNP / 北京 BJP（全市） |
| `-trains` | 全部 | 只监控这些车次，如 `G550,G600` |
| `-seat` | 二等座 | 二等座 / 一等座 / 商务座 / 无座 / 软卧 / 硬卧 / 硬座 |
| `-interval` | 30s | 查询间隔，别设到 10s 以下 |
| `-once` | 否 | 只查一次就退出 |

## 边界

- 不登录、不保存账号密码、不自动下单、不自动支付
- 不绕验证码、不换 IP/账号躲封禁、不高频秒刷
- 连续失败 3 次就退出，可能是被限流或 12306 接口变了，过一会儿再试
- 12306 的查询接口后缀会变（query / queryG / queryZ），程序会按返回的 `c_url` 自动跟；如果以后完全变了，需要按最新接口再改

***************
如需预订，需要加个油猴脚本

