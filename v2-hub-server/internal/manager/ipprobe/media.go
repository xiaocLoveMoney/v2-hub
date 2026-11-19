package ipprobe

import (
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// 流媒体与 AI 服务的键（docs/03 §5.23 流媒体与 AI）。
const (
	MediaNetflix        = "netflix"
	MediaYouTubePremium = "youtube_premium"
	MediaPrimeVideo     = "prime_video"
	MediaTikTok         = "tiktok"
	MediaReddit         = "reddit"
	MediaChatGPT        = "chatgpt"
)

// 服务状态。
const (
	StatusYes           = "yes"
	StatusNo            = "no"
	StatusOriginalsOnly = "originals_only"
	StatusWebOnly       = "web_only"
	StatusAppOnly       = "app_only"
	StatusFailed        = "failed"
	StatusUnknown       = "unknown"
)

// 解锁方式。
const (
	MethodNative  = "native"
	MethodDNS     = "dns"
	MethodUnknown = "unknown"
)

const regionChina = "CN"

// fetch 是一次 HTTP 探测的定义：Key 只含小写字母与数字（用作文件名与回传键）。
type fetch struct {
	Key   string
	URL   string
	Args  []string
	Marks []mark
	Grabs []grab
}

// mark 数正文里某个固定字符串出现的行数。
type mark struct{ Name, Text string }

// grab 取正文里第一处匹配某个 ERE 的片段（≤ 200 字节），由 Go 再从片段里取值。
type grab struct{ Name, Pattern string }

// mediaService 是一个服务：Domain 用于随机子域名检查，Judge 由各次请求的结果给出状态与地区。
type mediaService struct {
	Key     string
	Domain  string
	Primary string
	Fetches []fetch
	Judge   func(map[string]Fetch) (status, region string)
}

// MediaResult 是一个服务的结论；Evidence 是排查用的状态码与命中数，不参与对比。
type MediaResult struct {
	Key      string
	Status   string
	Region   string
	Method   string
	Evidence map[string]string
}

const acceptLanguageEN = "Accept-Language: en-US,en;q=0.9"

var (
	// regionPattern 从抠出的片段里取最后一个两位大写国家码。
	regionPattern = regexp.MustCompile(`[A-Z]{2}`)
	// netflixPathRegion 从 Netflix 跳转后的地址里取地区（/hk/title/… 或 /hk-en/title/…）。
	netflixPathRegion = regexp.MustCompile(`netflix\.com/([a-z]{2})(-[a-z]{2})?/title/`)
	ipPattern         = regexp.MustCompile(`[0-9a-fA-F:.]+$`)
)

// egressFetches 取出口 IP：先 Cloudflare 的 trace，再 icanhazip 兜底。
var egressFetches = []fetch{
	{Key: "eg", URL: "https://www.cloudflare.com/cdn-cgi/trace", Grabs: []grab{{"ip", `^ip=[0-9.]+`}, {"loc", `^loc=[A-Z]{2}`}}},
	{Key: "egb", URL: "https://ipv4.icanhazip.com/", Grabs: []grab{{"ip", `^[0-9.]+`}}},
}

// mediaServices 是检测的服务与判定表；站点改版时只改这里。特征串需在真机上核对（docs/03 §5.23 待人工验证）。
var mediaServices = []mediaService{
	{
		Key: MediaNetflix, Domain: "netflix.com", Primary: "nfo",
		Fetches: []fetch{
			// 81280792 是自制剧，70143836 是授权片：授权片可看才算完整解锁
			{Key: "nfo", URL: "https://www.netflix.com/title/81280792", Args: []string{"-L", "-H", acceptLanguageEN},
				Marks: []mark{{"oh", "Oh no!"}}, Grabs: []grab{{"rc", `"requestCountry":\{"id":"[A-Z]{2}"`}}},
			{Key: "nfl", URL: "https://www.netflix.com/title/70143836", Args: []string{"-L", "-H", acceptLanguageEN},
				Marks: []mark{{"oh", "Oh no!"}}},
		},
		Judge: judgeNetflix,
	},
	{
		Key: MediaYouTubePremium, Domain: "youtube.com", Primary: "yt",
		Fetches: []fetch{{Key: "yt", URL: "https://www.youtube.com/premium",
			Args:  []string{"-H", acceptLanguageEN, "-b", "CONSENT=YES+cb; SOCS=CAI"},
			Marks: []mark{{"cn", "www.google.cn"}, {"na", "Premium is not available in your country"}, {"af", "ad-free"}},
			Grabs: []grab{{"gl", `"INNERTUBE_CONTEXT_GL": ?"[A-Z]{2}"`}, {"cr", `"contentRegion": ?"[A-Z]{2}"`}}}},
		Judge: judgeYouTube,
	},
	{
		Key: MediaPrimeVideo, Domain: "primevideo.com", Primary: "pv",
		Fetches: []fetch{{Key: "pv", URL: "https://www.primevideo.com/", Args: []string{"-L", "-H", acceptLanguageEN},
			Grabs: []grab{{"ct", `"currentTerritory": ?"[A-Z]{2}"`}}}},
		Judge: judgePrimeVideo,
	},
	{
		Key: MediaTikTok, Domain: "tiktok.com", Primary: "tt",
		Fetches: []fetch{{Key: "tt", URL: "https://www.tiktok.com/", Args: []string{"-L", "-H", acceptLanguageEN},
			Grabs: []grab{{"rg", `"region": ?"[A-Z]{2}"`}}}},
		Judge: judgeTikTok,
	},
	{
		Key: MediaReddit, Domain: "reddit.com", Primary: "rdt",
		Fetches: []fetch{{Key: "rdt", URL: "https://www.reddit.com/", Args: []string{"-L", "-H", acceptLanguageEN}}},
		Judge:   judgeReddit,
	},
	{
		Key: MediaChatGPT, Domain: "chatgpt.com", Primary: "cgw",
		Fetches: []fetch{
			// 网页端：合规接口在不支持的地区返回 unsupported_country；App 端：被拒时页面提示关掉 VPN
			{Key: "cgw", URL: "https://api.openai.com/compliance/cookie_requirements", Marks: []mark{{"uc", "unsupported_country"}}},
			{Key: "cga", URL: "https://ios.chat.openai.com/", Marks: []mark{{"vpn", "VPN"}}},
			{Key: "cgt", URL: "https://chatgpt.com/cdn-cgi/trace", Grabs: []grab{{"loc", `^loc=[A-Z]{2}`}}},
		},
		Judge: judgeChatGPT,
	},
}

func judgeMedia(svc mediaService, fetches map[string]Fetch, randomResolvable bool) MediaResult {
	status, region := svc.Judge(fetches)
	out := MediaResult{Key: svc.Key, Status: status, Region: region, Method: MethodUnknown, Evidence: evidence(svc, fetches)}
	if usable(status) {
		out.Method = unlockMethod(fetches[svc.Primary], randomResolvable)
	}
	return out
}

// usable 报告状态是不是“能用”（含部分可用）。
func usable(status string) bool {
	switch status {
	case StatusYes, StatusOriginalsOnly, StatusWebOnly, StatusAppOnly:
		return true
	default:
		return false
	}
}

// Usable 同 usable，供 service 计数。
func Usable(status string) bool { return usable(status) }

func unlockMethod(primary Fetch, randomResolvable bool) string {
	if primary.RemoteIP.IsValid() && isInternal(primary.RemoteIP) {
		return MethodDNS
	}
	if randomResolvable {
		return MethodDNS
	}
	if primary.RemoteIP.IsValid() {
		return MethodNative
	}
	return MethodUnknown
}

func evidence(svc mediaService, fetches map[string]Fetch) map[string]string {
	out := make(map[string]string, len(svc.Fetches)*2)
	for _, def := range svc.Fetches {
		f := fetches[def.Key]
		out[def.Key+".exit"] = strconv.Itoa(f.Exit)
		out[def.Key+".code"] = strconv.Itoa(f.Code)
		for _, m := range def.Marks {
			out[def.Key+"."+m.Name] = strconv.Itoa(f.Marks[m.Name])
		}
	}
	return out
}

// regionOf 从片段里取国家码，取不到为空串。
func regionOf(snippet string) string {
	all := regionPattern.FindAllString(snippet, -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

// allFailed 报告这些请求是不是全都没拿到响应。
func allFailed(fs ...Fetch) bool {
	for _, f := range fs {
		if f.OK() {
			return false
		}
	}
	return true
}

func judgeNetflix(fs map[string]Fetch) (string, string) {
	orig, licensed := fs["nfo"], fs["nfl"]
	if allFailed(orig, licensed) {
		return StatusFailed, ""
	}
	region := regionOf(orig.Grabs["rc"])
	if region == "" {
		if m := netflixPathRegion.FindStringSubmatch(orig.URL); m != nil {
			region = strings.ToUpper(m[1])
		}
	}
	if orig.Code == http.StatusForbidden || licensed.Code == http.StatusForbidden {
		return StatusNo, region
	}
	available := func(f Fetch) bool { return f.OK() && f.Code < http.StatusBadRequest && f.Marks["oh"] == 0 }
	unavailable := func(f Fetch) bool { return f.OK() && (f.Code == http.StatusNotFound || f.Marks["oh"] > 0) }
	switch {
	case available(licensed):
		return StatusYes, region
	case available(orig) && unavailable(licensed):
		return StatusOriginalsOnly, region
	case unavailable(orig) && unavailable(licensed):
		return StatusNo, region
	default:
		return StatusUnknown, region
	}
}

func judgeYouTube(fs map[string]Fetch) (string, string) {
	f := fs["yt"]
	if !f.OK() {
		return StatusFailed, ""
	}
	region := regionOf(f.Grabs["gl"])
	if region == "" {
		region = regionOf(f.Grabs["cr"])
	}
	switch {
	case f.Marks["cn"] > 0:
		return StatusNo, regionChina
	case f.Marks["na"] > 0:
		return StatusNo, region
	case f.Marks["af"] > 0:
		return StatusYes, region
	case f.Code == http.StatusForbidden:
		return StatusNo, region
	default:
		return StatusUnknown, region
	}
}

func judgePrimeVideo(fs map[string]Fetch) (string, string) {
	f := fs["pv"]
	if !f.OK() {
		return StatusFailed, ""
	}
	if region := regionOf(f.Grabs["ct"]); region != "" {
		return StatusYes, region
	}
	return StatusNo, ""
}

func judgeTikTok(fs map[string]Fetch) (string, string) {
	f := fs["tt"]
	if !f.OK() {
		return StatusFailed, ""
	}
	if region := regionOf(f.Grabs["rg"]); region != "" {
		return StatusYes, region
	}
	if f.Code == http.StatusForbidden {
		return StatusNo, ""
	}
	return StatusUnknown, ""
}

func judgeReddit(fs map[string]Fetch) (string, string) {
	f := fs["rdt"]
	switch {
	case !f.OK():
		return StatusFailed, ""
	case f.Code == http.StatusOK:
		return StatusYes, ""
	case f.Code == http.StatusForbidden:
		return StatusNo, ""
	default:
		return StatusUnknown, ""
	}
}

func judgeChatGPT(fs map[string]Fetch) (string, string) {
	web, app, trace := fs["cgw"], fs["cga"], fs["cgt"]
	region := ""
	if trace.OK() {
		region = regionOf(trace.Grabs["loc"])
	}
	if allFailed(web, app) {
		return StatusFailed, region
	}
	webOK := web.OK() && web.Marks["uc"] == 0 && web.Code != http.StatusForbidden
	appOK := app.OK() && app.Marks["vpn"] == 0 && app.Code != http.StatusForbidden
	switch {
	case webOK && appOK:
		return StatusYes, region
	case webOK:
		return StatusWebOnly, region
	case appOK:
		return StatusAppOnly, region
	default:
		return StatusNo, region
	}
}

// judgeEgress 取出口 IP 与国家：trace 优先，icanhazip 兜底；不是合法 IPv4 的不要。
func judgeEgress(kv map[string]string) Egress {
	fs := fetchesOf(kv, egressFetches)
	out := Egress{}
	for _, key := range []string{"eg", "egb"} {
		f := fs[key]
		if !f.OK() {
			continue
		}
		raw := ipPattern.FindString(f.Grabs["ip"])
		if ip, err := netip.ParseAddr(raw); err == nil && ip.Is4() {
			out.IP = ip
			break
		}
	}
	if f := fs["eg"]; f.OK() {
		out.Country = regionOf(f.Grabs["loc"])
	}
	return out
}
