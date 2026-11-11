// main.go
// 12306 余票监控桌面客户端（常驻稳定版）
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	stationJSURL = "https://kyfw.12306.cn/otn/resources/js/framework/station_name.js"
	initURL      = "https://kyfw.12306.cn/otn/leftTicket/init?linktypeid=dc"
	baseOTN      = "https://kyfw.12306.cn/otn/"
	userAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
)

var nameToCode = map[string]string{
	"北京": "BJP", "北京南": "VNP", "北京西": "BXP", "北京丰台": "FTP", "北京朝阳": "IFP", "北京北": "VAP",
	"上海": "SHH", "上海虹桥": "AOH", "上海南": "SNH", "上海西": "SXH",
	"广州": "GZQ", "广州南": "IZQ", "广州东": "GGQ", "广州白云": "GBQ",
	"深圳": "SZQ", "深圳北": "IOQ", "深圳东": "BJQ", "福田": "NZQ",
	"南京": "NJH", "南京南": "NKH", "杭州": "HZH", "杭州东": "HGH", "杭州西": "HVH",
	"苏州": "SZH", "苏州北": "OHH", "无锡": "WXH", "武汉": "WHN", "汉口": "HKN", "武昌": "WCN",
	"长沙": "CSQ", "长沙南": "CWQ", "成都东": "ICW", "成都南": "CNW", "重庆北": "CUW", "重庆西": "CXW",
	"西安北": "EAY", "西安": "XAY", "郑州东": "ZAF", "郑州": "ZZF", "天津": "TJP", "天津西": "TXP",
}

var codeToName = map[string]string{}
var stationMu sync.RWMutex

type leftTicketResp struct {
	HTTPStatus int    `json:"httpstatus"`
	Status     bool   `json:"status"`
	CURL       string `json:"c_url"`
	Data       struct {
		Result []string          `json:"result"`
		Flag   string            `json:"flag"`
		Map    map[string]string `json:"map"`
	} `json:"data"`
}

type TrainRow struct {
	TrainCode  string            `json:"trainCode"`
	FromName   string            `json:"fromName"`
	ToName     string            `json:"toName"`
	StartTime  string            `json:"startTime"`
	ArriveTime string            `json:"arriveTime"`
	Duration   string            `json:"duration"`
	CanBuy     bool              `json:"canBuy"`
	Seats      map[string]string `json:"seats"`
	Prices     map[string]string `json:"prices"`
}

type QueryResponse struct {
	Success  bool       `json:"success"`
	Message  string     `json:"message"`
	FromCode string     `json:"fromCode"`
	ToCode   string     `json:"toCode"`
	FromName string     `json:"fromName"`
	ToName   string     `json:"toName"`
	BuyURL   string     `json:"buyUrl"`
	TimeStr  string     `json:"timeStr"`
	Trains   []TrainRow `json:"trains"`
}

type querier struct {
	mu         sync.Mutex
	cookieFile string
	inited     bool
	queryPath  string
}

func newQuerier() (*querier, error) {
	f, err := os.CreateTemp("", "12306-gui-cookie-*")
	if err != nil {
		return nil, err
	}
	f.Close()
	return &querier{
		cookieFile: f.Name(),
		queryPath:  "leftTicket/queryG",
	}, nil
}

func (q *querier) curlGet(rawURL, referer, extraCookie string) ([]byte, error) {
	args := []string{
		"-s", "-L", "--max-time", "12",
		"-c", q.cookieFile, "-b", q.cookieFile,
		"-H", "User-Agent: " + userAgent,
		"-H", "Accept: application/json, text/javascript, */*; q=0.01",
		"-H", "Accept-Language: zh-CN,zh;q=0.9",
		"-H", "X-Requested-With: XMLHttpRequest",
	}
	if referer != "" {
		args = append(args, "-H", "Referer: "+referer)
	}
	if extraCookie != "" {
		args = append(args, "-H", "Cookie: "+extraCookie)
	}
	args = append(args, rawURL)

	cmd := exec.Command("curl", args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("网络请求失败: %v", err)
	}
	return out, nil
}

func hideWindow(cmd *exec.Cmd) {
	if runtime.GOOS == "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}
}

func (q *querier) loadStations() {
	stationMu.Lock()
	for name, code := range nameToCode {
		codeToName[code] = name
	}
	stationMu.Unlock()

	b, err := q.curlGet(stationJSURL, "", "")
	if err != nil {
		return
	}
	parts := strings.Split(string(b), "@")
	stationMu.Lock()
	defer stationMu.Unlock()
	for _, p := range parts[1:] {
		fields := strings.Split(p, "|")
		if len(fields) >= 3 {
			name := strings.TrimSpace(fields[1])
			code := strings.TrimSpace(fields[2])
			if name != "" && code != "" {
				nameToCode[name] = code
				codeToName[code] = name
			}
		}
	}
}

func resolveStation(input string) (string, string) {
	input = strings.TrimSpace(input)
	stationMu.RLock()
	defer stationMu.RUnlock()

	if code, ok := nameToCode[input]; ok {
		return input, code
	}
	trimmed := strings.TrimSuffix(input, "站")
	if code, ok := nameToCode[trimmed]; ok {
		return trimmed, code
	}
	upper := strings.ToUpper(input)
	if name, ok := codeToName[upper]; ok {
		return name, upper
	}
	for name, code := range nameToCode {
		if strings.Contains(name, trimmed) {
			return name, code
		}
	}
	return "", ""
}

func extractAllPrices(f []string) map[string]string {
	res := map[string]string{}
	if len(f) <= 39 || len(f[39]) < 10 {
		return res
	}
	info := f[39]
	rawMap := map[string]string{}
	for i := 0; i+10 <= len(info); i += 10 {
		chunk := info[i : i+10]
		code := chunk[0:1]
		rawPrice, err := strconv.Atoi(chunk[1:6])
		if err != nil || rawPrice <= 0 {
			continue
		}
		var pStr string
		if rawPrice%10 == 0 {
			pStr = fmt.Sprintf("¥%d", rawPrice/10)
		} else {
			pStr = fmt.Sprintf("¥%.1f", float64(rawPrice)/10.0)
		}
		rawMap[code] = pStr
		if strings.HasPrefix(chunk[6:10], "3") {
			rawMap["W"] = pStr
		}
	}

	pick := func(codes ...string) string {
		for _, c := range codes {
			if v, ok := rawMap[c]; ok {
				return v
			}
		}
		return ""
	}

	res["swz"] = pick("9", "P")
	res["ydz"] = pick("M")
	res["edz"] = pick("O", "S")
	res["rw"] = pick("4", "I")
	res["yw"] = pick("3", "J")
	res["yz"] = pick("1")
	res["wz"] = pick("W", "O", "1")
	return res
}

func (q *querier) queryTickets(date, fromInput, toInput string) QueryResponse {
	q.mu.Lock()
	defer q.mu.Unlock()

	fromName, fromCode := resolveStation(fromInput)
	toName, toCode := resolveStation(toInput)
	if fromCode == "" || toCode == "" {
		return QueryResponse{Success: false, Message: "未能识别出发站或到达站名称，请输入准确的中文站名（如：上海虹桥）"}
	}

	buyURL := fmt.Sprintf("https://kyfw.12306.cn/otn/leftTicket/init?linktypeid=dc&fs=%s,%s&ts=%s,%s&date=%s&flag=N,N,Y",
		url.QueryEscape(fromName), fromCode, url.QueryEscape(toName), toCode, date)

	if !q.inited {
		if _, err := q.curlGet(initURL, "", ""); err == nil {
			q.inited = true
		}
	}

	candidates := []string{q.queryPath, "leftTicket/queryG", "leftTicket/queryZ", "leftTicket/query"}
	var body *leftTicketResp
	var lastErr error

	seen := map[string]bool{}
	for _, path := range candidates {
		if seen[path] {
			continue
		}
		seen[path] = true
		b, err := doQuery(q, baseOTN+path, date, fromCode, toCode)
		if err != nil {
			lastErr = err
			continue
		}
		if !b.Status && b.CURL != "" {
			seen[b.CURL] = true
			b, err = doQuery(q, baseOTN+b.CURL, date, fromCode, toCode)
			if err == nil && b.Status {
				q.queryPath = b.CURL
				body = b
				break
			}
		}
		if b.Status {
			q.queryPath = path
			body = b
			break
		}
	}

	if body == nil || !body.Status {
		q.inited = false
		msg := "12306 未返回有效数据（请确认日期在 15 天预售期内）"
		if lastErr != nil {
			msg = lastErr.Error()
		}
		return QueryResponse{Success: false, Message: msg}
	}

	stationMu.Lock()
	for k, v := range body.Data.Map {
		codeToName[k] = v
	}
	stationMu.Unlock()

	var rows []TrainRow
	for _, raw := range body.Data.Result {
		f := strings.Split(raw, "|")
		if len(f) <= 32 {
			continue
		}
		stationMu.RLock()
		sFrom, sTo := codeToName[f[6]], codeToName[f[7]]
		stationMu.RUnlock()
		if sFrom == "" {
			sFrom = f[6]
		}
		if sTo == "" {
			sTo = f[7]
		}

		valOrDash := func(idx int) string {
			if idx < len(f) && f[idx] != "" {
				return f[idx]
			}
			return "--"
		}

		seats := map[string]string{
			"swz": valOrDash(32),
			"ydz": valOrDash(31),
			"edz": valOrDash(30),
			"rw":  valOrDash(23),
			"yw":  valOrDash(28),
			"yz":  valOrDash(29),
			"wz":  valOrDash(26),
		}

		rows = append(rows, TrainRow{
			TrainCode:  f[3],
			FromName:   sFrom,
			ToName:     sTo,
			StartTime:  f[8],
			ArriveTime: f[9],
			Duration:   f[10],
			CanBuy:     f[11] == "Y",
			Seats:      seats,
			Prices:     extractAllPrices(f),
		})
	}

	return QueryResponse{
		Success:  true,
		FromCode: fromCode,
		ToCode:   toCode,
		FromName: fromName,
		ToName:   toName,
		BuyURL:   buyURL,
		TimeStr:  time.Now().Format("15:04:05"),
		Trains:   rows,
	}
}

func doQuery(q *querier, base, date, from, to string) (*leftTicketResp, error) {
	rawURL := base + "?leftTicketDTO.train_date=" + date +
		"&leftTicketDTO.from_station=" + from +
		"&leftTicketDTO.to_station=" + to +
		"&purpose_codes=ADULT"
	extraCookie := fmt.Sprintf("_jc_save_fromStation=%s; _jc_save_toStation=%s; _jc_save_fromDate=%s; _jc_save_toDate=%s; _jc_save_wfdc_flag=dc",
		from, to, date, date)

	b, err := q.curlGet(rawURL, initURL, extraCookie)
	if err != nil {
		return nil, err
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	var out leftTicketResp
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("解析响应失败")
	}
	return &out, nil
}

func openURLInBrowser(targetURL string) {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL)
		hideWindow(cmd)
		_ = cmd.Start()
		return
	}
	_ = exec.Command("open", targetURL).Start()
}

func main() {
	q, err := newQuerier()
	if err != nil {
		return
	}
	go q.loadStations()

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(htmlPage))
	})

	mux.HandleFunc("/api/query", func(w http.ResponseWriter, r *http.Request) {
		date := r.URL.Query().Get("date")
		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		resp := q.queryTickets(date, from, to)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/api/open", func(w http.ResponseWriter, r *http.Request) {
		u := r.URL.Query().Get("url")
		if strings.HasPrefix(u, "https://kyfw.12306.cn/") {
			openURLInBrowser(u)
		}
		w.WriteHeader(http.StatusOK)
	})

	// 优先绑定固定端口 12306，若被占用则使用随机端口
	ln, err := net.Listen("tcp", "127.0.0.1:12306")
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return
		}
	}
	appURL := fmt.Sprintf("http://%s", ln.Addr().String())

	go func() {
		time.Sleep(200 * time.Millisecond)
		openURLInBrowser(appURL)
	}()

	// 移除了自动退出机制，服务器将一直常驻运行，永不失效！
	_ = http.Serve(ln, mux)
}

const htmlPage = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<title>中国铁路12306 - 智能余票雷达监控桌面客户端</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; font-family: "Microsoft YaHei", "PingFang SC", sans-serif; }
  body { background: #f5f7fa; color: #333; font-size: 14px; user-select: none; }
  
  .header { background: #fff; padding: 12px 24px; display: flex; align-items: center; justify-content: space-between; border-bottom: 1px solid #eaeaea; }
  .brand { display: flex; align-items: center; gap: 12px; }
  .logo-circle { width: 38px; height: 38px; border-radius: 50%; background: #e60012; color: #fff; font-weight: bold; font-size: 20px; display: flex; align-items: center; justify-content: center; }
  .brand-title { font-size: 20px; font-weight: 700; color: #333; }
  .brand-sub { font-size: 11px; color: #888; letter-spacing: 1px; }
  .header-right { display: flex; align-items: center; gap: 12px; }
  .status-badge { font-size: 13px; padding: 6px 14px; border-radius: 20px; background: #eef5fe; color: #1e78d2; font-weight: 600; }

  .navbar { background: #3b99fc; height: 40px; display: flex; align-items: center; padding: 0 24px; color: #fff; gap: 28px; font-size: 14px; }
  .navbar span { cursor: pointer; padding: 8px 14px; border-radius: 4px; }
  .navbar span.active { background: #2676e3; font-weight: bold; }

  .container { max-width: 1180px; margin: 14px auto; padding: 0 14px; }
  .search-box { background: #eef1f8; border: 1px solid #bad0e8; border-radius: 4px; padding: 14px 18px; display: flex; flex-wrap: wrap; align-items: center; gap: 14px; justify-content: space-between; }
  .field-group { display: flex; align-items: center; gap: 8px; }
  .field-group label { font-size: 13px; color: #555; font-weight: 600; }
  .field-group input, .field-group select { height: 32px; padding: 0 10px; border: 1px solid #cfcfcf; border-radius: 3px; background: #fff; font-size: 13px; width: 125px; outline: none; }
  .field-group input:focus, .field-group select:focus { border-color: #3b99fc; }
  .swap-btn { width: 28px; height: 28px; border-radius: 50%; border: 1px solid #3b99fc; color: #3b99fc; background: #fff; cursor: pointer; font-weight: bold; display: flex; align-items: center; justify-content: center; transition: 0.2s; }
  .swap-btn:hover { background: #3b99fc; color: #fff; }

  .btn-group { display: flex; gap: 10px; }
  .btn { height: 34px; padding: 0 16px; border: none; border-radius: 4px; cursor: pointer; font-size: 13px; font-weight: bold; color: #fff; transition: 0.2s; }
  .btn-query { background: #ff8000; }
  .btn-query:hover { background: #e67300; }
  .btn-monitor { background: #26a65b; }
  .btn-monitor.running { background: #e74c3c; }
  .btn-buy { background: #3b99fc; }
  .btn-buy:hover { background: #2676e3; }

  .filter-box { background: #fff; border: 1px solid #d2e1f1; margin-top: 10px; padding: 10px 16px; border-radius: 4px; display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px; }
  .filter-left { display: flex; align-items: center; gap: 14px; flex-wrap: wrap; font-size: 13px; }
  .filter-left label { display: flex; align-items: center; gap: 4px; cursor: pointer; }
  .tag-all { background: #3b99fc; color: #fff; padding: 2px 8px; border-radius: 3px; font-size: 12px; }

  .table-wrap { margin-top: 10px; background: #fff; border: 1px solid #c7d9ec; border-radius: 4px; overflow: hidden; box-shadow: 0 2px 6px rgba(0,0,0,0.04); }
  table { width: 100%; border-collapse: collapse; text-align: center; }
  thead th { background: linear-gradient(180deg, #3587d8 0%, #246bb8 100%); color: #fff; font-weight: 600; font-size: 13px; padding: 10px 6px; border-right: 1px solid #4894e0; }
  tbody tr { border-bottom: 1px solid #e6eef8; transition: 0.15s; }
  tbody tr:nth-child(even) { background: #f8fbff; }
  tbody tr:hover { background: #eef6ff; }
  tbody tr.highlight-hit { background: #e8f8ee !important; }
  tbody td { padding: 10px 6px; border-right: 1px solid #edf2f8; font-size: 13px; }

  .train-no { font-weight: 700; font-size: 15px; color: #1f6bc4; }
  .st-dep { font-weight: 600; color: #333; }
  .st-arr { color: #666; font-size: 12px; margin-top: 3px; }
  .tm-dep { font-weight: 700; font-size: 14px; color: #333; }
  .tm-arr { color: #888; font-size: 12px; margin-top: 3px; }
  .dur { color: #555; font-weight: 600; }

  .tk-yes { color: #26a65b; font-weight: 700; }
  .tk-num { color: #26a65b; font-weight: 700; font-size: 14px; }
  .tk-wait { color: #ff8000; font-weight: 600; }
  .tk-none { color: #bbb; }
  .price-sub { display: block; font-size: 11px; color: #ff6600; font-weight: normal; margin-top: 2px; }

  .book-link { display: inline-block; padding: 5px 14px; background: #3b99fc; color: #fff; border-radius: 3px; text-decoration: none; font-size: 13px; cursor: pointer; font-weight: bold; }
  .book-link:hover { background: #1e70d6; }
  .book-disabled { color: #bbb; font-size: 12px; }

  .footer-bar { margin-top: 10px; padding: 10px 16px; background: #fff; border: 1px solid #dce6f2; border-radius: 4px; display: flex; justify-content: space-between; align-items: center; font-size: 13px; color: #555; }
  .hit-banner { display: none; margin-top: 10px; padding: 12px 18px; background: #e8f8ee; border: 2px solid #26a65b; border-radius: 4px; color: #1b7e43; font-weight: bold; align-items: center; justify-content: space-between; }
</style>
</head>
<body>

<div class="header">
  <div class="brand">
    <div class="logo-circle">铁</div>
    <div>
      <div class="brand-title">中国铁路12306 · 余票雷达</div>
      <div class="brand-sub">12306 TICKET MONITOR DESKTOP (常驻稳定版)</div>
    </div>
  </div>
  <div class="header-right">
    <button class="btn btn-buy" onclick="openLoginPage()" title="点击直接打开 12306 登录页扫码登录">👤 提前登录 12306 账号</button>
    <div class="status-badge" id="statusBadge">🟢 就绪：等待查询或启动监控</div>
  </div>
</div>

<div class="navbar">
  <span>首页</span>
  <span class="active">车票余票监控</span>
  <span>实时票价解析</span>
  <span>一键直达乘车人页面</span>
</div>

<div class="container">
  <div class="search-box">
    <div class="field-group">
      <label>出发地</label>
      <input type="text" id="fromStation" value="上海虹桥" placeholder="中文站名/代码">
      <button class="swap-btn" onclick="swapStations()" title="互换出发到达站">⇄</button>
      <label>目的地</label>
      <input type="text" id="toStation" value="北京南" placeholder="中文站名/代码">
    </div>

    <div class="field-group">
      <label>出发日</label>
      <input type="date" id="trainDate">
    </div>

    <div class="field-group">
      <label>监控席别</label>
      <select id="watchSeat" onchange="renderTable()">
        <option value="edz" selected>二等座</option>
        <option value="ydz">一等座</option>
        <option value="swz">商务座</option>
        <option value="wz">无座</option>
        <option value="yw">硬卧</option>
        <option value="rw">软卧</option>
        <option value="yz">硬座</option>
      </select>
    </div>

    <div class="field-group">
      <label>轮询间隔</label>
      <select id="intervalSec">
        <option value="15">15 秒</option>
        <option value="30" selected>30 秒</option>
        <option value="45">45 秒</option>
        <option value="60">60 秒</option>
      </select>
    </div>

    <div class="btn-group">
      <button class="btn btn-query" onclick="doQueryOnce()">立即查询</button>
      <button class="btn btn-monitor" id="monitorBtn" onclick="toggleMonitor()">启动自动监控</button>
    </div>
  </div>

  <div class="filter-box">
    <div class="filter-left">
      <strong>车次类型：</strong>
      <span class="tag-all">支持筛选</span>
      <label><input type="checkbox" class="type-chk" value="G" checked onchange="renderTable()"> 高铁(G)</label>
      <label><input type="checkbox" class="type-chk" value="D" checked onchange="renderTable()"> 动车(D)</label>
      <label><input type="checkbox" class="type-chk" value="C" checked onchange="renderTable()"> 城际(C)</label>
      <label><input type="checkbox" class="type-chk" value="Z" checked onchange="renderTable()"> 直达(Z)</label>
      <label><input type="checkbox" class="type-chk" value="T" checked onchange="renderTable()"> 特快(T)</label>
      <label><input type="checkbox" class="type-chk" value="K" checked onchange="renderTable()"> 快速(K)</label>
      <label><input type="checkbox" id="showPriceChk" checked onchange="renderTable()"> 显示实时票价</label>
    </div>
    <div class="field-group">
      <label>指定车次：</label>
      <input type="text" id="trainFilter" placeholder="如 G1,G2 (留空全部)" oninput="renderTable()" style="width:160px;">
    </div>
  </div>

  <div class="hit-banner" id="hitBanner">
    <span id="hitText">🎉 发现可用余票！</span>
    <button class="btn btn-buy" id="fastBookBtn" onclick="openOfficialBuy(window.firstHitTrain || '')">立即直达预订首趟有票车次 ➔</button>
  </div>

  <div class="table-wrap">
    <table>
      <thead>
        <tr>
          <th style="width:90px;">车次</th>
          <th style="width:130px;">出发站<br>到达站</th>
          <th style="width:110px;">出发时间<br>到达时间</th>
          <th style="width:90px;">历时</th>
          <th>商务座<br>特等座</th>
          <th>一等座</th>
          <th>二等座<br>二等包座</th>
          <th>软卧/动卧<br>一等卧</th>
          <th>硬卧<br>二等卧</th>
          <th>硬座</th>
          <th>无座</th>
          <th style="width:95px;">备注</th>
        </tr>
      </thead>
      <tbody id="ticketTbody">
        <tr><td colspan="12" style="padding:36px;color:#888;">首次使用建议先点右上角【提前登录 12306 账号】，然后点击【立即查询】或【启动自动监控】</td></tr>
      </tbody>
    </table>
  </div>

  <div class="footer-bar">
    <span id="logText">提示：有票时自动响铃，点击任意车次右侧的【预订】即可自动跳转进入乘车人确认页！</span>
    <span id="lastTimeText">最后更新：--:--:--</span>
  </div>
</div>

<script>
  const tomorrow = new Date(Date.now() + 86400000);
  document.getElementById('trainDate').value = tomorrow.toISOString().split('T')[0];

  let lastData = null;
  let monitorTimer = null;
  let countdownTimer = null;
  let remainSec = 0;
  window.firstHitTrain = '';

  function swapStations() {
    const f = document.getElementById('fromStation');
    const t = document.getElementById('toStation');
    const tmp = f.value;
    f.value = t.value;
    t.value = tmp;
  }

  function playAlertBeep() {
    try {
      const ctx = new (window.AudioContext || window.webkitAudioContext)();
      [0, 0.25, 0.5].forEach(offset => {
        const osc = ctx.createOscillator();
        const gain = ctx.createGain();
        osc.type = 'sine';
        osc.frequency.value = 880;
        gain.gain.setValueAtTime(0.25, ctx.currentTime + offset);
        gain.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + offset + 0.2);
        osc.connect(gain);
        gain.connect(ctx.destination);
        osc.start(ctx.currentTime + offset);
        osc.stop(ctx.currentTime + offset + 0.2);
      });
    } catch (e) {}
  }

  async function doQueryOnce() {
    const date = document.getElementById('trainDate').value;
    const from = document.getElementById('fromStation').value.trim();
    const to = document.getElementById('toStation').value.trim();

    document.getElementById('logText').innerText = '正在向 12306 查询最新余票与票价...';
    try {
      const res = await fetch('/api/query?date=' + encodeURIComponent(date) + '&from=' + encodeURIComponent(from) + '&to=' + encodeURIComponent(to));
      const data = await res.json();
      if (!data.success) {
        document.getElementById('logText').innerText = '⚠️ 查询失败：' + data.message;
        return;
      }
      lastData = data;
      document.getElementById('fromStation').value = data.fromName;
      document.getElementById('toStation').value = data.toName;
      document.getElementById('lastTimeText').innerText = '最后更新：' + data.timeStr + '（共 ' + data.trains.length + ' 趟列车）';
      renderTable();
    } catch (err) {
      document.getElementById('logText').innerText = '⚠️ 网络请求异常：' + err;
    }
  }

  function isTicketAvailable(val) {
    if (!val) return false;
    if (val === '有') return true;
    return /^[0-9]+$/.test(val);
  }

  function formatCell(val, price, showPrice) {
    let cls = 'tk-none';
    if (val === '有') cls = 'tk-yes';
    else if (/^[0-9]+$/.test(val)) cls = 'tk-num';
    else if (val === '候补') cls = 'tk-wait';

    let pHtml = (showPrice && price) ? '<span class="price-sub">' + price + '</span>' : '';
    return '<span class="' + cls + '">' + (val || '--') + '</span>' + pHtml;
  }

  function renderTable() {
    if (!lastData || !lastData.trains) return;
    const tbody = document.getElementById('ticketTbody');
    const watchSeat = document.getElementById('watchSeat').value;
    const showPrice = document.getElementById('showPriceChk').checked;

    const checkedTypes = Array.from(document.querySelectorAll('.type-chk:checked')).map(cb => cb.value);
    const customTrains = document.getElementById('trainFilter').value.toUpperCase().replace(/，/g, ',').split(',').map(s => s.trim()).filter(Boolean);

    let html = '';
    let hitTrains = [];
    window.firstHitTrain = '';

    for (const tr of lastData.trains) {
      const firstChar = tr.trainCode.charAt(0);
      if (checkedTypes.length > 0 && !checkedTypes.includes(firstChar) && ['G','D','C','Z','T','K'].includes(firstChar)) {
        continue;
      }
      if (customTrains.length > 0 && !customTrains.includes(tr.trainCode.toUpperCase())) {
        continue;
      }

      const seatVal = tr.seats[watchSeat];
      const hit = isTicketAvailable(seatVal);
      if (hit) {
        if (!window.firstHitTrain) window.firstHitTrain = tr.trainCode;
        hitTrains.push(tr.trainCode + '(' + seatVal + ')');
      }

      html += '<tr class="' + (hit ? 'highlight-hit' : '') + '">';
      html += '<td class="train-no">' + tr.trainCode + '</td>';
      html += '<td><div class="st-dep">' + tr.fromName + '</div><div class="st-arr">' + tr.toName + '</div></td>';
      html += '<td><div class="tm-dep">' + tr.startTime + '</div><div class="tm-arr">' + tr.arriveTime + '</div></td>';
      html += '<td class="dur">' + tr.duration + '</td>';
      html += '<td>' + formatCell(tr.seats.swz, tr.prices.swz, showPrice) + '</td>';
      html += '<td>' + formatCell(tr.seats.ydz, tr.prices.ydz, showPrice) + '</td>';
      html += '<td>' + formatCell(tr.seats.edz, tr.prices.edz, showPrice) + '</td>';
      html += '<td>' + formatCell(tr.seats.rw, tr.prices.rw, showPrice) + '</td>';
      html += '<td>' + formatCell(tr.seats.yw, tr.prices.yw, showPrice) + '</td>';
      html += '<td>' + formatCell(tr.seats.yz, tr.prices.yz, showPrice) + '</td>';
      html += '<td>' + formatCell(tr.seats.wz, tr.prices.wz, showPrice) + '</td>';
      html += '<td>' + (tr.canBuy ? '<a class="book-link" onclick="openOfficialBuy(\'' + tr.trainCode + '\')">预订</a>' : '<span class="book-disabled">候补/无</span>') + '</td>';
      html += '</tr>';
    }

    tbody.innerHTML = html || '<tr><td colspan="12" style="padding:30px;color:#999;">没有符合当前筛选条件的车次</td></tr>';

    const banner = document.getElementById('hitBanner');
    if (hitTrains.length > 0) {
      banner.style.display = 'flex';
      document.getElementById('hitText').innerText = '🎉 发现目标席别有余票：' + hitTrains.slice(0, 8).join('、') + (hitTrains.length > 8 ? ' 等' : '');
      document.getElementById('fastBookBtn').innerText = '立即直达预订 【' + window.firstHitTrain + '】 ➔';
      document.getElementById('logText').innerText = '✅ 已发现可用余票，点击右侧【预订】即可自动跳转进入乘车人确认页！';
      playAlertBeep();
    } else {
      banner.style.display = 'none';
      document.getElementById('logText').innerText = '查询完毕：当前筛选条件下目标席别暂无余票。';
    }
  }

  function toggleMonitor() {
    const btn = document.getElementById('monitorBtn');
    const badge = document.getElementById('statusBadge');

    if (monitorTimer) {
      clearInterval(monitorTimer);
      clearInterval(countdownTimer);
      monitorTimer = null;
      countdownTimer = null;
      btn.innerText = '启动自动监控';
      btn.classList.remove('running');
      badge.innerText = '🟢 已停止自动监控';
      return;
    }

    const sec = parseInt(document.getElementById('intervalSec').value, 10) || 30;
    remainSec = sec;
    btn.innerText = '停止监控 (' + remainSec + 's)';
    btn.classList.add('running');
    badge.innerText = '🔄 雷达监控运行中（每 ' + sec + ' 秒刷新）';

    doQueryOnce();

    countdownTimer = setInterval(() => {
      remainSec--;
      if (remainSec <= 0) remainSec = sec;
      btn.innerText = '停止监控 (' + remainSec + 's)';
    }, 1000);

    monitorTimer = setInterval(() => {
      remainSec = sec;
      doQueryOnce();
    }, sec * 1000);
  }

  function openOfficialBuy(trainCode) {
    if (lastData && lastData.buyUrl) {
      let targetUrl = lastData.buyUrl;
      if (trainCode) {
        targetUrl += '#auto_book=' + encodeURIComponent(trainCode);
      }
      fetch('/api/open?url=' + encodeURIComponent(targetUrl));
    } else {
      const date = document.getElementById('trainDate').value;
      fetch('/api/open?url=' + encodeURIComponent('https://kyfw.12306.cn/otn/leftTicket/init?linktypeid=dc&date=' + date));
    }
  }

  function openLoginPage() {
    fetch('/api/open?url=' + encodeURIComponent('https://kyfw.12306.cn/otn/resources/login.html'));
  }
</script>
</body>
</html>`
