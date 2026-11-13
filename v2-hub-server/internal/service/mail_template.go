package service

import (
	"bytes"
	"fmt"
	htmltpl "html/template"
	texttpl "text/template"
)

// 邮件模板内置在后端（docs/03 §5.7 内容）：纯文本与简单 HTML 各一份，变量只做转义后的文本替换。

// mailVars 是模板变量。
type mailVars struct {
	Site    string
	Code    string
	Minutes int
	Action  string // 验证码用途的说明，如“注册账号”
	Title   string // 公告标题、工单主题
	Body    string // 公告正文（Markdown 源文本，按纯文本发出）
	// 以下用于续费与提醒邮件（docs/03 §5.16）；金额与时间都是已经格式化好的文本
	Plan    string
	Amount  string
	Balance string
	Expires string
	Reason  string
	Note    string // 到期提醒里关于自动续费的一句说明
	Remain  string // 剩余流量
}

// mailContent 是渲染后的一封邮件。
type mailContent struct {
	Subject string
	Text    string
	HTML    string
}

type mailTemplate struct {
	subject *texttpl.Template
	text    *texttpl.Template
	html    *htmltpl.Template
}

const mailHTMLWrap = `<div style="font-family:-apple-system,'Segoe UI',sans-serif;font-size:14px;color:#34395e;line-height:1.7">%s<p style="color:#98a6ad;font-size:12px">— {{.Site}}</p></div>`

func mustMailTemplate(name, subject, text, html string) mailTemplate {
	return mailTemplate{
		subject: texttpl.Must(texttpl.New(name + ".subject").Parse(subject)),
		text:    texttpl.Must(texttpl.New(name + ".text").Parse(text)),
		html:    htmltpl.Must(htmltpl.New(name + ".html").Parse(fmt.Sprintf(mailHTMLWrap, html))),
	}
}

var (
	mailTplCode = mustMailTemplate("code",
		"【{{.Site}}】验证码 {{.Code}}",
		"你正在{{.Action}}，验证码是 {{.Code}}，{{.Minutes}} 分钟内有效。\n如果不是你本人操作，请忽略这封邮件。\n\n— {{.Site}}\n",
		`<p>你正在{{.Action}}，验证码是</p><p style="font-size:24px;font-weight:700;letter-spacing:4px;color:#6777ef">{{.Code}}</p><p>{{.Minutes}} 分钟内有效。如果不是你本人操作，请忽略这封邮件。</p>`)
	mailTplTest = mustMailTemplate("test",
		"【{{.Site}}】测试邮件",
		"这是一封测试邮件，说明 SMTP 配置可用。\n\n— {{.Site}}\n",
		`<p>这是一封测试邮件，说明 SMTP 配置可用。</p>`)
	mailTplAnnouncement = mustMailTemplate("announcement",
		"【{{.Site}}】{{.Title}}",
		"{{.Title}}\n\n{{.Body}}\n\n— {{.Site}}\n",
		`<h3>{{.Title}}</h3><div style="white-space:pre-wrap">{{.Body}}</div>`)
	mailTplTicketReply = mustMailTemplate("ticket_reply",
		"【{{.Site}}】工单有新回复",
		"你的工单“{{.Title}}”有新回复，请登录后查看。\n\n— {{.Site}}\n",
		`<p>你的工单“{{.Title}}”有新回复，请登录后查看。</p>`)
	mailTplRenewOK = mustMailTemplate("renew_ok",
		"【{{.Site}}】已自动续费",
		"你的套餐“{{.Plan}}”已自动续费，扣款 {{.Amount}}，新的到期时间是 {{.Expires}}，当前余额 {{.Balance}}。\n\n— {{.Site}}\n",
		`<p>你的套餐“{{.Plan}}”已自动续费。</p><p>扣款 {{.Amount}}，新的到期时间是 {{.Expires}}，当前余额 {{.Balance}}。</p>`)
	mailTplRenewFailed = mustMailTemplate("renew_failed",
		"【{{.Site}}】自动续费失败",
		"你的套餐“{{.Plan}}”已到期，自动续费没有成功：{{.Reason}}。\n自动续费已关闭，节点已停用；充值后不会补扣，请登录后重新购买。\n\n— {{.Site}}\n",
		`<p>你的套餐“{{.Plan}}”已到期，自动续费没有成功：{{.Reason}}。</p><p>自动续费已关闭，节点已停用；充值后不会补扣，请登录后重新购买。</p>`)
	mailTplExpireSoon = mustMailTemplate("expire_soon",
		"【{{.Site}}】套餐即将到期",
		"你的套餐“{{.Plan}}”将在 {{.Expires}} 到期。{{.Note}}\n\n— {{.Site}}\n",
		`<p>你的套餐“{{.Plan}}”将在 {{.Expires}} 到期。</p><p>{{.Note}}</p>`)
	mailTplTrafficLow = mustMailTemplate("traffic_low",
		"【{{.Site}}】流量即将用完",
		"你的套餐流量只剩 {{.Remain}}，用完后节点会停用。可以登录后购买流量包或续费。\n\n— {{.Site}}\n",
		`<p>你的套餐流量只剩 {{.Remain}}，用完后节点会停用。</p><p>可以登录后购买流量包或续费。</p>`)
	mailTplTicketNew = mustMailTemplate("ticket_new",
		"【{{.Site}}】工单待处理",
		"工单“{{.Title}}”有用户的新消息，请到管理端处理。\n\n— {{.Site}}\n",
		`<p>工单“{{.Title}}”有用户的新消息，请到管理端处理。</p>`)
)

func renderMail(t mailTemplate, v mailVars) (mailContent, error) {
	var subject, text, html bytes.Buffer
	if err := t.subject.Execute(&subject, v); err != nil {
		return mailContent{}, fmt.Errorf("render mail subject: %w", err)
	}
	if err := t.text.Execute(&text, v); err != nil {
		return mailContent{}, fmt.Errorf("render mail text: %w", err)
	}
	if err := t.html.Execute(&html, v); err != nil {
		return mailContent{}, fmt.Errorf("render mail html: %w", err)
	}
	return mailContent{Subject: subject.String(), Text: text.String(), HTML: html.String()}, nil
}
