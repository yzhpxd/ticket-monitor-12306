// ticket-monitor.go
// 12306 余票监控提醒（全中文交互 + 全国中文站名自动识别 + 防闪退版）
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	stationJSURL = "https://kyfw.12306.cn/otn/resources/js/framework/station_name.js"
	initURL      = "https://kyfw.12306.cn/otn/leftTicket/init?linktypeid=dc"
	baseOTN      = "https://kyfw.12306.cn/otn/"
	userAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
)

// 内置常见车站兜底（启动时会自动从 12306 官网拉取全国 3000+ 车站覆盖）
var nameToCode = map[string]string{
	"北京": "BJP", "北京南": "VNP", "北京西": "BXP", "北京丰台": "FTP", "北京朝阳": "IFP", "北京北": "VAP",
	"上海": "SHH", "上海虹桥": "AOH", "上海南": "SNH", "上海西": "SXH",
	"广州": "GZQ", "广州南": "IZQ", "广州东": "GGQ", "广州白云": "GBQ",
	"深圳": "SZQ", "深圳北": "IOQ", "深圳东": "BJQ", "福田": "NZQ",
	"南京": "NJH", "南京南": "NKH", "镇江": "ZJH", "镇江南": "ZEH",
	"杭州": "HZH", "杭州东": "HGH", "杭州西": "HVH", "杭州南": "XHH",
	"苏州": "SZH", "苏州北": "OHH", "无锡": "WXH", "无锡东": "WGH",
	"武汉": "WHN", "汉口": "HKN", "武昌": "WCN",
	"长沙": "CSQ", "长沙南": "CWQ", "成都东": "ICW", "成都南": "CNW", "成都西": "CMW",
	"重庆北": "CUW", "重庆西": "CXW", "西安北": "EAY", "西安": "XAY",
	"郑州东": "ZAF", "郑州": "ZZF", "天津": "TJP", "天津西": "TXP", "天津南": "TIP",
}

var codeToName = map[string]string{}

// 席别菜单与对应 12306 数组下标
type seatOption struct {
	Name  string
	Index int
}

var seatMenu = []seatOption{
	{"二等座", 30},
	{"一等座", 31},
	{"商务座", 32},
	{"无座", 26},
	{"硬卧", 28},
	{"软卧", 23},
	{"硬座", 29},
}

var seatIndexes = map[string]int{
	"二等座": 30, "1": 30,
	"一等座": 31, "2": 31,
	"商务座": 32, "3": 32,
	"无座": 26, "4": 26,
	"硬卧": 28, "5": 28,
	"软卧": 23, "6": 23,
	"硬座": 29, "7": 29,
}

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

type querier struct {
	cookieFile string
	inited     bool
	queryPath  string
}

func newQuerier() (*querier, error) {
	f, err := os.CreateTemp("", "12306-cookie-*")
	if err != nil {
		return nil, err
	}
	f.Close()
	return &querier{
		cookieFile: f.Name(),
		queryPath:  "leftTicket/queryG",
	}, nil
}

func (q *querier) close() { os.Remove(q.cookieFile) }

func (q *querier) curlGet(rawURL, referer, extraCookie string) ([]byte, error) {
	args := []string{
		"-s", "-L", "--max-time", "15",
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
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("curl 失败 (%v): %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// loadStations 从 12306 加载全国车站字典（格式：@bjb|北京北|VAP|beijingbei|bjb|0...）
func (q *querier) loadStations() {
	for name, code := range nameToCode {
		codeToName[code] = name
	}
	b, err := q.curlGet(stationJSURL, "", "")
	if err != nil {
		return
	}
	parts := strings.Split(string(b), "@")
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

func pauseExit(reader *bufio.Reader, code int) {
	fmt.Println("\n----------------------------------------")
	fmt.Print("程序已停止，按【回车键 Enter】关闭窗口...")
	reader.ReadBytes('\n')
	os.Exit(code)
}

func readLine(reader *bufio.Reader, prompt string) string {
	fmt.Print(prompt)
	s, _ := reader.ReadString('\n')
	return strings.TrimSpace(s)
}

// askStation 交互式询问车站，支持输入中文站名（如“上海虹桥”）或三字代码（如“AOH”）
func askStation(reader *bufio.Reader, label, defaultStation string) (string, string) {
	for {
		input := readLine(reader, fmt.Sprintf("请输入%s（中文站名或代码，直接回车默认【%s】）: ", label, defaultStation))
		if input == "" {
			input = defaultStation
		}
		// 1. 直接匹配中文站名
		if code, ok := nameToCode[input]; ok {
			return input, code
		}
		// 2. 去掉末尾的“站”字再试一次（例如用户输入“北京南站”）
		trimmed := strings.TrimSuffix(input, "站")
		if code, ok := nameToCode[trimmed]; ok {
			return trimmed, code
		}
		// 3. 输入的是三字英文代码（如 VNP）
		upper := strings.ToUpper(input)
		if name, ok := codeToName[upper]; ok {
			return name, upper
		}
		// 4. 模糊匹配提示
		var matches []string
		for name, code := range nameToCode {
			if strings.Contains(name, trimmed) {
				matches = append(matches, fmt.Sprintf("%s(%s)", name, code))
				if len(matches) >= 8 {
					break
				}
			}
		}
		if len(matches) > 0 {
			fmt.Printf("  ⚠️ 未找到精确站名 %q，你是不是想找：%s\n", input, strings.Join(matches, "、"))
		} else {
			fmt.Printf("  ⚠️ 未识别的车站 %q，请重新输入（也可以直接输入 12306 三字大写车站代码）。\n", input)
		}
	}
}

func askDate(reader *bufio.Reader) string {
	defaultDate := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	for {
		input := readLine(reader, fmt.Sprintf("请输入出发日期 YYYY-MM-DD（直接回车默认明天【%s】）: ", defaultDate))
		if input == "" {
			return defaultDate
		}
		if _, err := time.Parse("2006-01-02", input); err != nil {
			fmt.Println("  ⚠️ 日期格式不对，请按 2026-11-11 这种格式输入。")
			continue
		}
		return input
	}
}

func askSeat(reader *bufio.Reader) (string, int) {
	fmt.Println("可选监控席别：")
	for i, opt := range seatMenu {
		fmt.Printf("  [%d] %s", i+1, opt.Name)
		if i == 0 {
			fmt.Print(" (默认)")
		}
		fmt.Println()
	}
	for {
		input := readLine(reader, "请选择席别（输入数字 1-7 或席别名称，直接回车默认【二等座】）: ")
		if input == "" {
			return "二等座", 30
		}
		if idx, ok := seatIndexes[input]; ok {
			for _, opt := range seatMenu {
				if opt.Index == idx {
					return opt.Name, opt.Index
				}
			}
		}
		fmt.Println("  ⚠️ 输入无效，请输入数字 1 到 7，或者席别中文名（如：二等座、硬卧）。")
	}
}

func askInterval(reader *bufio.Reader) time.Duration {
	for {
		input := readLine(reader, "请输入查询间隔秒数（最少 10 秒，直接回车默认【30】秒）: ")
		if input == "" {
			return 30 * time.Second
		}
		sec, err := strconv.Atoi(strings.TrimSuffix(input, "s"))
		if err != nil || sec <= 0 {
			fmt.Println("  ⚠️ 请输入有效的秒数数字，比如 30。")
			continue
		}
		if sec < 10 {
			fmt.Println("  ⚠️ 间隔太短会被 12306 封 IP，已自动设为最低 10 秒。")
			sec = 10
		}
		return time.Duration(sec) * time.Second
	}
}

func main() {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("==================================================")
	fmt.Println("          12306 余票监控提醒（交互式个人版）")
	fmt.Println("==================================================")

	if _, err := exec.LookPath("curl"); err != nil {
		fmt.Println("❌ 找不到 curl：请确认 Windows 10/11 系统自带 curl.exe 可用。")
		pauseExit(reader, 1)
	}

	q, err := newQuerier()
	if err != nil {
		fmt.Printf("❌ 创建临时文件失败：%v\n", err)
		pauseExit(reader, 1)
	}
	defer q.close()

	fmt.Print("正在加载全国车站列表...")
	q.loadStations()
	fmt.Printf(" 完成（共 %d 个车站）\n\n", len(nameToCode))

	// 1. 交互输入：日期、出发站、到达站、车次、席别、间隔
	date := askDate(reader)
	fromName, fromCode := askStation(reader, "出发站", "上海虹桥")
	toName, toCode := askStation(reader, "到达站", "北京南")
	trainsInput := readLine(reader, "指定监控车次（多个用逗号分隔如 G1,G2；直接回车监控【全部车次】）: ")
	trainsInput = strings.ReplaceAll(trainsInput, "，", ",")
	seatName, seatIdx := askSeat(reader)
	interval := askInterval(reader)

	trainTip := "全部车次"
	if trainsInput != "" {
		trainTip = strings.ToUpper(trainsInput)
	}

	fmt.Println("\n==================================================")
	fmt.Printf("📌 监控任务已启动：\n")
	fmt.Printf("   日期：%s\n", date)
	fmt.Printf("   区间：%s(%s) → %s(%s)\n", fromName, fromCode, toName, toCode)
	fmt.Printf("   车次：%s | 席别：%s | 间隔：%v\n", trainTip, seatName, interval)
	fmt.Println("==================================================")
	fmt.Println("提示：发现有票会响铃并停留在窗口；按 Ctrl+C 可随时终止。")

	failures := 0
	for {
		found, waitlist, err := queryOnce(q, date, fromCode, toCode, trainsInput, seatIdx, seatName)
		if err != nil {
			failures++
			q.inited = false
			fmt.Printf("[%s] 查询失败（%d 次）：%v\n", time.Now().Format("15:04:05"), failures, err)
			if failures >= 3 {
				fmt.Println("❌ 连续失败 3 次，请检查日期是否在 15 天预售期内，或稍后再试。")
				pauseExit(reader, 1)
			}
			time.Sleep(interval)
			continue
		}
		failures = 0

		if len(waitlist) > 0 {
			fmt.Printf("[%s] 候补中：%s\n", time.Now().Format("15:04:05"), strings.Join(waitlist, "；"))
		}
		if len(found) > 0 {
			fmt.Print("\a\a\a\n")
			fmt.Println("==================================================")
			fmt.Println("🎉 有票了！请马上打开 12306 官网或 APP 下单：")
			for _, f := range found {
				fmt.Println("  ", f)
			}
			fmt.Println("==================================================")
			pauseExit(reader, 0)
		}
		fmt.Printf("[%s] 暂无余票，%v 后继续查询…\n", time.Now().Format("15:04:05"), interval)
		time.Sleep(interval)
	}
}

func queryOnce(q *querier, date, from, to, trains string, seatIdx int, seatName string) ([]string, []string, error) {
	if !q.inited {
		if _, err := q.curlGet(initURL, "", ""); err != nil {
			return nil, nil, fmt.Errorf("init 失败：%w", err)
		}
		q.inited = true
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

		b, err := doQuery(q, baseOTN+path, date, from, to)
		if err != nil {
			lastErr = err
			continue
		}
		if !b.Status && b.CURL != "" {
			seen[b.CURL] = true
			b, err = doQuery(q, baseOTN+b.CURL, date, from, to)
			if err != nil {
				lastErr = err
				continue
			}
			if b.Status {
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
		if lastErr != nil {
			return nil, nil, lastErr
		}
		return nil, nil, fmt.Errorf("12306 未返回有效车次数据（请确认日期在 15 天预售期内）")
	}

	for k, v := range body.Data.Map {
		codeToName[k] = v
	}

	want := map[string]bool{}
	if trains != "" {
		for _, t := range strings.Split(trains, ",") {
			t = strings.TrimSpace(strings.ToUpper(t))
			if t != "" {
				want[t] = true
			}
		}
	}

	var found, waitlist []string
	for _, raw := range body.Data.Result {
		f := strings.Split(raw, "|")
		if len(f) <= seatIdx {
			continue
		}
		code := f[3]
		if len(want) > 0 && !want[code] {
			continue
		}

		sFrom, sTo := codeToName[f[6]], codeToName[f[7]]
		if sFrom == "" {
			sFrom = f[6]
		}
		if sTo == "" {
			sTo = f[7]
		}

		ticketVal := f[seatIdx]
		desc := fmt.Sprintf("%-5s %s→%s (%s-%s) %s：%s",
			code, sFrom, sTo, f[8], f[9], seatName, ticketVal)

		switch {
		case ticketVal == "有" || isNumber(ticketVal):
			found = append(found, desc)
		case ticketVal == "无" && len(f) > 37 && f[37] == "1":
			waitlist = append(waitlist, desc+"(可候补)")
		case ticketVal == "候补":
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

	extraCookie := fmt.Sprintf("_jc_save_fromStation=%s; _jc_save_toStation=%s; _jc_save_fromDate=%s; _jc_save_toDate=%s; _jc_save_wfdc_flag=dc",
		from, to, date, date)

	b, err := q.curlGet(rawURL, initURL, extraCookie)
	if err != nil {
		return nil, err
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})

	var out leftTicketResp
	if err := json.Unmarshal(b, &out); err != nil {
		preview := strings.TrimSpace(string(b))
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, fmt.Errorf("非 JSON 响应（%d 字节）", len(b))
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
