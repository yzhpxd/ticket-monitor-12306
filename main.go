// ticket-monitor.go
// 12306 余票监控提醒（个人自用版）
//
// 只做一件事：定时查询 12306 公开余票接口，发现指定车次/席别有票就
// 在终端大声提醒你，然后退出，由你自己去 12306 官网/APP 下单。
//
// 不做、也不会做的事：
//   - 不登录、不保存账号密码、不自动下单、不自动支付
//   - 不绕过验证码、不换 IP/换账号躲封禁、不高频秒刷
//   - 查询间隔默认 30 秒，遇到限流/验证就退避并停下来
//   - 只查你自己的 1 张票，别拿去给别人批量抢票
//
// 为什么用 curl 而不用 Go 自带的 http 客户端？
// 12306 会按 TLS 指纹识别客户端，Go 标准库的 TLS 指纹会被直接挡回
// 一个 2042 字节的 HTML 验证页，curl（Windows 10/11 自带 curl.exe）
// 的指纹能正常拿到 JSON。所以本程序调系统自带的 curl 来查票，
// Windows 上请确认命令行里能跑 `curl --version`，Linux/macOS 同理。
//
// 用法：
//
//	go build -o ticket-monitor.exe .
//	ticket-monitor.exe -date 2026-11-11 -from ZEH -to VNP -trains G550,G600,G738 -seat 二等座
//	ticket-monitor.exe -once   // 只查一次就退出，用来测试
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// 12306 余票查询入口。先 GET init 拿 cookie，再按 c_url 给的后缀查。
	// 接口后缀会变（query / queryG / queryZ …），程序会按返回的 c_url 自动跟。
	initURL   = "https://kyfw.12306.cn/otn/leftTicket/init"
	queryURL  = "https://kyfw.12306.cn/otn/leftTicket/query"
	userAgent = "Mozilla/5.0"
)

// 常用站代码（2026-11-10 在 12306 station_name.js 核对过）：
// 镇江 ZJH，镇江南 ZEH，北京 BJP（全市），北京南 VNP，北京丰台 FTP，北京西 BXP
var stationNames = map[string]string{
	"ZJH": "镇江", "ZEH": "镇江南", "BJP": "北京",
	"VNP": "北京南", "FTP": "北京丰台", "BXP": "北京西",
}

// 12306 返回的每条车次是 | 分隔的字符串，席别余票在这些下标：
// 二等座 30，一等座 31，商务座 32，无座 26，软卧 23，硬卧 28，硬座 29
var seatIndexes = map[string]int{
	"二等座": 30, "er": 30, "second": 30,
	"一等座": 31, "yi": 31, "first": 31,
	"商务座": 32, "sw": 32, "business": 32,
	"无座": 26, "wz": 26,
	"软卧": 23, "rw": 23,
	"硬卧": 28, "yw": 28,
	"硬座": 29, "yz": 29,
}

type leftTicketResp struct {
	HTTPStatus int    `json:"httpstatus"`
	Status     bool   `json:"status"`
	CURL       string `json:"c_url"`
	Data       struct {
		Result []string `json:"result"`
		Flag   string   `json:"flag"`
	} `json:"data"`
}

// querier 用系统 curl 查票，cookie 存在临时文件里，程序退出时删除。
type querier struct {
	cookieFile string
}

func newQuerier() (*querier, error) {
	f, err := os.CreateTemp("", "12306-cookie-*")
	if err != nil {
		return nil, err
	}
	f.Close()
	return &querier{cookieFile: f.Name()}, nil
}

func (q *querier) close() { os.Remove(q.cookieFile) }

// curlGet 用 curl 发一个 GET，返回响应体。超时 15 秒。
func (q *querier) curlGet(rawURL, referer string) ([]byte, error) {
	args := []string{"-s", "--max-time", "15",
		"-c", q.cookieFile, "-b", q.cookieFile,
		"-H", "User-Agent: " + userAgent,
		rawURL}
	if referer != "" {
		args = append(args, "-H", "Referer: "+referer)
	}
	cmd := exec.Command("curl", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("curl 失败（请确认系统装了 curl）：%w", err)
	}
	return out, nil
}

func main() {
	date := flag.String("date", "2026-11-11", "出发日期 YYYY-MM-DD")
	from := flag.String("from", "ZEH", "出发站代码：镇江南 ZEH / 镇江 ZJH")
	to := flag.String("to", "VNP", "到达站代码：北京南 VNP / 北京 BJP（全市）")
	trains := flag.String("trains", "", "只监控这些车次，逗号分隔，如 G550,G600,G738；留空监控全部")
	seat := flag.String("seat", "二等座", "席别：二等座 / 一等座 / 商务座 / 无座 / 软卧 / 硬卧 / 硬座")
	interval := flag.Duration("interval", 30*time.Second, "查询间隔，默认 30s；别设到 10s 以下，会被限流")
	once := flag.Bool("once", false, "只查一次就退出")
	flag.Parse()

	seatIdx, ok := seatIndexes[strings.ToLower(*seat)]
	if !ok {
		fmt.Printf("不支持的席别 %q，可选：二等座/一等座/商务座/无座/软卧/硬卧/硬座\n", *seat)
		os.Exit(1)
	}
	if *interval < 10*time.Second {
		fmt.Println("间隔太短会被 12306 限流，已强制改成 10 秒。")
		*interval = 10 * time.Second
	}
	if _, err := exec.LookPath("curl"); err != nil {
		fmt.Println("找不到 curl：Windows 10/11 自带 curl.exe，请在命令行里先跑 `curl --version` 确认。")
		os.Exit(1)
	}
	q, err := newQuerier()
	if err != nil {
		fmt.Printf("创建 cookie 临时文件失败：%v\n", err)
		os.Exit(1)
	}
	defer q.close()

	fmt.Printf("监控：%s(%s) → %s(%s)，%s，席别 %s，间隔 %v\n",
		stationNames[*from], *from, stationNames[*to], *to, *date, *seat, *interval)
	fmt.Println("有票会响铃提醒并退出；没票就继续等，按 Ctrl+C 停止。")
	fmt.Println("注意：本程序不登录、不下单，发现有票请自己去 12306 下单。")

	failures := 0
	for {
		found, waitlist, err := queryOnce(q, *date, *from, *to, *trains, seatIdx, *seat)
		if err != nil {
			failures++
			fmt.Printf("[%s] 查询失败（%d 次）：%v\n", time.Now().Format("15:04:05"), failures, err)
			if failures >= 3 {
				fmt.Println("连续失败 3 次，可能是被限流或接口变了，程序退出，请过一会儿再试。")
				os.Exit(1)
			}
			time.Sleep(*interval * 2)
			continue
		}
		failures = 0
		if len(waitlist) > 0 {
			fmt.Printf("[%s] 候补中：%s\n", time.Now().Format("15:04:05"), strings.Join(waitlist, "；"))
		}
		if len(found) > 0 {
			fmt.Print("\a\a\a\n")
			fmt.Println("========================================")
			fmt.Println("有票了！请马上打开 12306 下单：")
			for _, f := range found {
				fmt.Println("  ", f)
			}
			fmt.Println("========================================")
			return
		}
		fmt.Printf("[%s] 暂无票，%v 后再查…\n", time.Now().Format("15:04:05"), *interval)
		if *once {
			return
		}
		time.Sleep(*interval)
	}
}

// queryOnce 查一次，返回有票的车次和候补的车次。
func queryOnce(q *querier, date, from, to, trains string, seatIdx int, seatName string) ([]string, []string, error) {
	// 1. 先访问 init 拿 cookie，否则 query 接口会直接返回 status:false
	if _, err := q.curlGet(initURL, ""); err != nil {
		return nil, nil, fmt.Errorf("init 失败：%w", err)
	}

	// 2. 查票，接口后缀可能变：先调 query，返回的 c_url 告诉你真正的后缀
	body, err := doQuery(q, queryURL, date, from, to)
	if err != nil {
		return nil, nil, err
	}
	if !body.Status && body.CURL != "" {
		realURL := "https://kyfw.12306.cn/otn/" + body.CURL
		body, err = doQuery(q, realURL, date, from, to)
		if err != nil {
			return nil, nil, err
		}
	}
	if !body.Status || len(body.Data.Result) == 0 {
		return nil, nil, fmt.Errorf("12306 返回无数据（status=%v，可能是接口变了或被限流）", body.Status)
	}

	want := map[string]bool{}
	if trains != "" {
		for _, t := range strings.Split(trains, ",") {
			want[strings.TrimSpace(strings.ToUpper(t))] = true
		}
	}

	var found, waitlist []string
	for _, raw := range body.Data.Result {
		f := strings.Split(raw, "|")
		if len(f) <= seatIdx {
			continue
		}
		code := f[3] // 车次，如 G550
		if len(want) > 0 && !want[code] {
			continue
		}
		// f[6] 出发站代码，f[7] 到达站代码，f[8] 出发时间，f[9] 到达时间
		desc := fmt.Sprintf("%s %s %s→%s %s-%s %s：%s",
			code, stationNames[f[6]], f[6], stationNames[f[7]], f[8], f[9], seatName, f[seatIdx])
		switch {
		case f[seatIdx] == "有" || isNumber(f[seatIdx]):
			found = append(found, desc)
		case f[seatIdx] == "候补":
			waitlist = append(waitlist, desc)
		}
	}
	return found, waitlist, nil
}

func doQuery(q *querier, base, date, from, to string) (*leftTicketResp, error) {
	rawURL := base + "?leftTicketDTO.train_date=" + date +
		"&leftTicketDTO.from_station=" + from +
		"&leftTicketDTO.to_station=" + to +
		"&purpose_codes=ADULT"
	b, err := q.curlGet(rawURL, initURL)
	if err != nil {
		return nil, err
	}
	// 12306 有时会在 JSON 前加 UTF-8 BOM，直接解析会报错，先去掉
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	var out leftTicketResp
	if err := json.Unmarshal(b, &out); err != nil {
		preview := string(b)
		if len(preview) > 300 {
			preview = preview[:300]
		}
		return nil, fmt.Errorf("解析 12306 返回失败：%w，原始内容前 300 字节：%q", err, preview)
	}
	return &out, nil
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
