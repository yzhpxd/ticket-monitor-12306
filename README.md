# 12306 余票监控提醒（桌面交互 + 自动直达订单版）

一款基于 Go 开发的个人自用 12306 余票监控工具。自带原生级 Windows 桌面图形交互界面与实时票价解析，监测到有票时自动响铃；配合配套的浏览器脚本，点击【预订】可绕过手动翻查与多次点击，毫秒级直达 12306 官方订单乘车人确认页。

---

## 核心特性

- **图形化操作**：纯桌面窗口运行，无黑色控制台窗口残留；支持直接输入中文站名（如 `上海虹桥`、`北京南`），自动模糊匹配全国 3400+ 车站。
- **实时票价解析**：直接解析官方余票接口内嵌票价数据，同步展示商务座、一等座、二等座、卧铺等各席别的实时价格。
- **一键直达订单**：发现有票时高亮并响铃；点击【预订】通过油猴脚本触发真实鼠标事件，秒级直达 `confirmPassenger` 订单确认页。
- **安全合规**：程序本身不收集账号密码、不代扣款、不走灰产接口，所有登录与支付流程均在官方网页原生安全环境中进行。

---

## 前置环境准备

### 1. 系统 curl 支持
Windows 10/11 默认自带 `curl.exe`。请在终端执行以下命令确认：
```bash
curl --version
```
> **原理**：12306 具有基于 TLS 指纹的防护拦截机制，Go 语言标准库的网络指纹会被挡回验证页。本程序底层调用系统级 curl 发送查询，保障数据获取的稳定性。

### 2. 浏览器油猴（Tampermonkey）配置
1. 在 Chrome / Edge 浏览器中安装 **Tampermonkey（篡改猴）** 扩展。
2. **关键权限开启（必须）**：
   - 打开 Chrome 扩展程序管理页：`chrome://extensions/`。
   - 开启右上角的 **【开发者模式】**。
   - 找到 **篡改猴** 卡片，点击 **【详情】**。
   - 将 **【允许运行用户脚本】（Allow User Scripts）** 拨动开关开启。
   - 确认 **网站访问权限** 设为 **“在所有网站上”**。

---

## 配套油猴直达脚本

打开篡改猴管理面板，点击 **添加新脚本**，清空默认模板后粘贴以下代码并保存（`Ctrl + S`）：

```javascript
// ==UserScript==
// @name         12306 余票雷达 - 自动直达乘车人页面
// @namespace    http://tampermonkey.net/
// @version      3.1
// @description  监控软件打开页面后，自动点查询并触发预订，直达乘车人确认页
// @match        *://kyfw.12306.cn/otn/leftTicket/*
// @run-at       document-idle
// @grant        none
// ==/UserScript==

(function() {
    'use strict';

    const hash = window.location.hash;
    if (!hash || !hash.includes('auto_book=')) return;

    const match = hash.match(/auto_book=([^&]+)/);
    if (!match) return;
    const targetTrain = decodeURIComponent(match[1]).trim().toUpperCase();

    let attempts = 0;
    let queryTriggered = false;

    const timer = setInterval(() => {
        attempts++;
        if (attempts > 120) {
            clearInterval(timer);
            return;
        }

        // 1. 若表格尚未出数据，自动点击查询
        const rows = document.querySelectorAll('#queryLeftTable tr');
        if (rows.length === 0 && !queryTriggered && attempts > 3) {
            const qBtn = document.getElementById('query_ticket');
            if (qBtn) {
                queryTriggered = true;
                qBtn.click();
            }
            return;
        }

        // 2. 表格渲染后，定位目标车次行并派发真实鼠标事件触发预订
        const trainLinks = document.querySelectorAll('#queryLeftTable a.number');
        for (const a of trainLinks) {
            if (a.innerText.trim().toUpperCase() === targetTrain) {
                const tr = a.closest('tr');
                if (!tr) continue;

                const btn = tr.querySelector('a.btn72');
                if (btn) {
                    clearInterval(timer);
                    btn.dispatchEvent(new MouseEvent('click', {
                        bubbles: true,
                        cancelable: true,
                        view: window
                    }));
                    return;
                }
            }
        }
    }, 100);
})();
```

---

## 编译运行（Go）

### 1. 本机编译（Windows）
在项目根目录下打开终端执行：
```bash
# -H=windowsgui 参数可消除黑色命令行控制台弹窗，纯 GUI 启动
go build -ldflags="-s -w -H=windowsgui" -o ticket-monitor.exe main.go
```

### 2. 跨平台交叉编译
如果你在 Linux 或 macOS 开发，可交叉编译出 Windows 可执行文件：
```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H=windowsgui" -o ticket-monitor.exe main.go
```

---

## 使用指南

1. 双击运行编译生成的 `ticket-monitor.exe`。
2. 首次启动建议点击界面右上角 **【👤 提前登录 12306 账号】**，在浏览器中扫码登录保持会话有效。
3. 输入**出发地**、**目的地**（支持中文站名缩写）、**出发日期**，选择需要监控的**席别**与**轮询间隔**。
4. 点击 **【启动自动监控】**。
5. 当监控到目标席别放票时，软件会自动发出高频提示音并在首行高亮提醒。
6. 点击该车次右侧的 **【预订】** 按钮，浏览器会自动唤起并将你直接送达乘车人确认页，勾选乘客后即可立即提交订单。

---

## 边界与声明

1. 本软件为个人学习及辅助购票工具，不提供任何破解验证码、高频并发打码或代扣款等破坏铁路票务系统公平性的功能。
2. 轮询间隔建议设置在 15 秒以上，避免因请求频率过高触发 12306 临时 IP 限流。
3. 任何使用本工具产生的购票行为均需用户在官方网页确认并最终自主支付。
